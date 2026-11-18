// Package vendor downloads JDK archives from upstream vendors and verifies
// them against pinned OpenPGP keys before they can be repackaged.
package vendor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"ctyl/internal/launch"
	"ctyl/internal/policy"
)

type Archive struct {
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SigURL    string `json:"signature_url"`
	SHA256    string `json:"sha256"`
	File      string `json:"file"`
	TopDir    string `json:"top_dir"`
	Signer    string `json:"signer_fingerprint"`
	Published string `json:"published,omitempty"`
}

type Build struct {
	Distribution string            `json:"distribution"`
	Line         int               `json:"line"`
	Version      string            `json:"version"`
	Archives     []Archive         `json:"archives"`
	Release      map[string]string `json:"release"`
	License      string            `json:"license"`
}

type Fetcher struct {
	Client  *http.Client
	RootDir string // repository root, for key paths
	OutDir  string
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

var versionRE = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]*$`)

// LatestVersion reports the newest release version a vendor publishes for a line.
func (f *Fetcher) LatestVersion(d policy.Distribution, l policy.Line) (string, error) {
	switch d.Kind {
	case "corretto":
		var releases []struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
			Draft      bool   `json:"draft"`
		}
		if err := f.getJSON(policy.Expand(d.ReleasesAPI, l), &releases); err != nil {
			return "", err
		}
		for _, r := range releases {
			if !r.Prerelease && !r.Draft && versionRE.MatchString(r.TagName) {
				return r.TagName, nil
			}
		}
		return "", fmt.Errorf("corretto %d: no releases", l.Major)
	case "temurin":
		a, err := f.temurinAsset(d, l, "x86_64")
		if err != nil {
			return "", err
		}
		return a.version, nil
	}
	return "", fmt.Errorf("distribution kind %q has no vendor archives", d.Kind)
}

type temurinAsset struct {
	version, link, checksum, sigLink, published string
}

func (f *Fetcher) temurinAsset(d policy.Distribution, l policy.Line, arch string) (temurinAsset, error) {
	url := strings.ReplaceAll(policy.Expand(d.API, l), "{arch}", d.ArchNames[arch])
	var assets []struct {
		Binary struct {
			UpdatedAt string `json:"updated_at"`
			Package   struct {
				Link          string `json:"link"`
				Checksum      string `json:"checksum"`
				SignatureLink string `json:"signature_link"`
			} `json:"package"`
		} `json:"binary"`
		Version struct {
			OpenJDKVersion string `json:"openjdk_version"`
		} `json:"version"`
	}
	if err := f.getJSON(url, &assets); err != nil {
		return temurinAsset{}, err
	}
	if len(assets) != 1 {
		return temurinAsset{}, fmt.Errorf("temurin %d/%s: expected one asset, got %d", l.Major, arch, len(assets))
	}
	a := assets[0]
	return temurinAsset{
		version:   strings.TrimSuffix(a.Version.OpenJDKVersion, "-LTS"),
		link:      a.Binary.Package.Link,
		checksum:  a.Binary.Package.Checksum,
		sigLink:   a.Binary.Package.SignatureLink,
		published: a.Binary.UpdatedAt,
	}, nil
}

// Fetch downloads and verifies the archives of one vendor release for archs.
// version may be empty to select the latest release.
func (f *Fetcher) Fetch(d policy.Distribution, l policy.Line, version string, archs []string) (*Build, error) {
	keyring, err := f.keyring(d)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(f.OutDir, 0o755); err != nil {
		return nil, err
	}
	b := &Build{Distribution: d.Name, Line: l.Major, License: d.License}

	for _, arch := range archs {
		var a Archive
		var expectedSHA string
		switch d.Kind {
		case "corretto":
			if version == "" {
				if version, err = f.LatestVersion(d, l); err != nil {
					return nil, err
				}
			}
			if !versionRE.MatchString(version) {
				return nil, fmt.Errorf("invalid version %q", version)
			}
			r := strings.NewReplacer("{version}", version, "{arch}", d.ArchNames[arch])
			a = Archive{Arch: arch, URL: r.Replace(policy.Expand(d.ArchiveURL, l)), SigURL: r.Replace(policy.Expand(d.SignatureURL, l))}
		case "temurin":
			ta, err := f.temurinAsset(d, l, arch)
			if err != nil {
				return nil, err
			}
			if version != "" && ta.version != version {
				return nil, fmt.Errorf("temurin %d: latest is %s, %s requested (Adoptium's API only serves the latest)", l.Major, ta.version, version)
			}
			version = ta.version
			a = Archive{Arch: arch, URL: ta.link, SigURL: ta.sigLink, Published: ta.published}
			expectedSHA = ta.checksum
		default:
			return nil, fmt.Errorf("distribution %s has no vendor archives", d.Name)
		}
		a.File = filepath.Join(f.OutDir, fmt.Sprintf("%s-%d-%s.tar.gz", d.Name, l.Major, arch))
		if err := f.download(a.URL, a.File); err != nil {
			return nil, err
		}
		sig, err := f.get(a.SigURL)
		if err != nil {
			return nil, err
		}
		signer, err := verifySignature(keyring, a.File, sig)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.URL, err)
		}
		if !strings.EqualFold(signer, d.SigningFingerprint) {
			return nil, fmt.Errorf("%s: signed by %s, policy requires %s", a.URL, signer, d.SigningFingerprint)
		}
		a.Signer = signer
		if a.SHA256, err = sha256File(a.File); err != nil {
			return nil, err
		}
		if expectedSHA != "" && !strings.EqualFold(expectedSHA, a.SHA256) {
			return nil, fmt.Errorf("%s: sha256 %s does not match vendor metadata %s", a.URL, a.SHA256, expectedSHA)
		}
		rel, top, err := readRelease(a.File)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.URL, err)
		}
		a.TopDir = top
		if b.Release == nil {
			b.Release = rel
		} else if rel["JAVA_RUNTIME_VERSION"] != b.Release["JAVA_RUNTIME_VERSION"] || rel["SOURCE"] != b.Release["SOURCE"] {
			return nil, fmt.Errorf("architectures disagree on runtime version or source: %v vs %v", rel, b.Release)
		}
		if want := expectedArch(arch); rel["OS_ARCH"] != want {
			return nil, fmt.Errorf("%s: release OS_ARCH %q, expected %q", a.URL, rel["OS_ARCH"], want)
		}
		if rel["LIBC"] != "" && rel["LIBC"] != "gnu" {
			return nil, fmt.Errorf("%s: LIBC=%s, glibc build required", a.URL, rel["LIBC"])
		}
		b.Archives = append(b.Archives, a)
	}
	b.Version = version
	return b, nil
}

func expectedArch(arch string) string {
	if arch == "x86_64" {
		return "x86_64"
	}
	return "aarch64"
}

func (f *Fetcher) keyring(d policy.Distribution) (openpgp.EntityList, error) {
	data, err := os.ReadFile(filepath.Join(f.RootDir, d.SigningKey))
	if err != nil {
		return nil, err
	}
	kr, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.SigningKey, err)
	}
	for _, e := range kr {
		if strings.EqualFold(fmt.Sprintf("%X", e.PrimaryKey.Fingerprint), d.SigningFingerprint) {
			return openpgp.EntityList{e}, nil
		}
	}
	return nil, fmt.Errorf("%s does not contain key %s", d.SigningKey, d.SigningFingerprint)
}

func verifySignature(kr openpgp.EntityList, file string, sig []byte) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var signer *openpgp.Entity
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN")) {
		signer, err = openpgp.CheckArmoredDetachedSignature(kr, f, bytes.NewReader(sig), nil)
	} else {
		signer, err = openpgp.CheckDetachedSignature(kr, f, bytes.NewReader(sig), nil)
	}
	if err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}
	return fmt.Sprintf("%X", signer.PrimaryKey.Fingerprint), nil
}

func readRelease(archive string) (map[string]string, string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, "", errors.New("no release file in archive")
		}
		if err != nil {
			return nil, "", err
		}
		parts := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(parts) == 2 && parts[1] == "release" && h.Typeflag == tar.TypeReg {
			rel, err := launch.ParseRelease(io.LimitReader(tr, 1<<20))
			return rel, parts[0], err
		}
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (f *Fetcher) get(url string) ([]byte, error) {
	resp, err := f.do(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (f *Fetcher) getJSON(url string, v any) error {
	data, err := f.get(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (f *Fetcher) do(url string) (*http.Response, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("refusing non-https URL %q", url)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ctyl-release-tooling")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func (f *Fetcher) download(url, dest string) error {
	resp, err := f.do(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := dest + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(resp.Body, 2<<30)); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// SignatureCheck exposes verification for tests and for re-verifying cached
// archives.
func SignatureCheck(rootDir string, d policy.Distribution, file string, sig []byte) (string, error) {
	kr, err := (&Fetcher{RootDir: rootDir}).keyring(d)
	if err != nil {
		return "", err
	}
	return verifySignature(kr, file, sig)
}

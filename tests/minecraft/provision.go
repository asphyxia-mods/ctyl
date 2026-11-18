// Package minecraft provisions real Minecraft servers (downloaded from their
// official distribution endpoints with checksum verification) and runs them
// in the images under the panel daemons' container settings.
package minecraft

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func cacheDir() string {
	if d := os.Getenv("CTYL_MC_CACHE"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "ctyl-minecraft-cache")
}

func getJSON(u string, v any) error {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "ctyl-integration-tests")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// download fetches u into the cache, verifying the digest when one is known.
// algo is "sha1", "sha256", "sha512" or "" (trust on first use: the digest is
// logged so runs are comparable).
func download(u, name, algo, want string) (string, error) {
	dest := filepath.Join(cacheDir(), "downloads", name)
	if want != "" {
		if got, err := fileDigest(dest, algo); err == nil && strings.EqualFold(got, want) {
			return dest, nil
		}
	} else if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "ctyl-integration-tests")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	tmp := dest + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	if want != "" {
		got, err := fileDigest(tmp, algo)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(got, want) {
			os.Remove(tmp)
			return "", fmt.Errorf("%s: %s %s, expected %s", u, algo, got, want)
		}
	}
	return dest, os.Rename(tmp, dest)
}

func fileDigest(path, algo string) (string, error) {
	var h hash.Hash
	switch algo {
	case "sha1":
		h = sha1.New()
	case "sha512":
		h = sha512.New()
	default:
		h = sha256.New()
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func vanillaJar(version string) (string, error) {
	var manifest struct {
		Versions []struct{ ID, URL string } `json:"versions"`
	}
	if err := getJSON("https://piston-meta.mojang.com/mc/game/version_manifest_v2.json", &manifest); err != nil {
		return "", err
	}
	for _, v := range manifest.Versions {
		if v.ID != version {
			continue
		}
		var meta struct {
			Downloads struct {
				Server struct{ SHA1, URL string } `json:"server"`
			} `json:"downloads"`
		}
		if err := getJSON(v.URL, &meta); err != nil {
			return "", err
		}
		return download(meta.Downloads.Server.URL, "vanilla-"+version+".jar", "sha1", meta.Downloads.Server.SHA1)
	}
	return "", fmt.Errorf("vanilla %s not found", version)
}

func paperJar(version string) (string, error) {
	var build struct {
		ID        int `json:"id"`
		Downloads map[string]struct {
			Name      string            `json:"name"`
			URL       string            `json:"url"`
			Checksums map[string]string `json:"checksums"`
		} `json:"downloads"`
	}
	if err := getJSON(fmt.Sprintf("https://fill.papermc.io/v3/projects/paper/versions/%s/builds/latest", version), &build); err != nil {
		return "", err
	}
	d, ok := build.Downloads["server:default"]
	if !ok {
		return "", fmt.Errorf("paper %s: no server download", version)
	}
	return download(d.URL, d.Name, "sha256", d.Checksums["sha256"])
}

func velocityJar(version string) (string, error) {
	var build struct {
		Downloads map[string]struct {
			Name      string            `json:"name"`
			URL       string            `json:"url"`
			Checksums map[string]string `json:"checksums"`
		} `json:"downloads"`
	}
	if err := getJSON(fmt.Sprintf("https://fill.papermc.io/v3/projects/velocity/versions/%s/builds/latest", version), &build); err != nil {
		return "", err
	}
	d, ok := build.Downloads["server:default"]
	if !ok {
		return "", fmt.Errorf("velocity %s: no server download", version)
	}
	return download(d.URL, d.Name, "sha256", d.Checksums["sha256"])
}

func geyserJar(platform string) (string, error) {
	const builds = "https://download.geysermc.org/v2/projects/geyser/versions"
	var build struct {
		Version   string `json:"version"`
		Build     int    `json:"build"`
		Downloads map[string]struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"downloads"`
	}
	if err := getJSON(builds+"/latest/builds/latest", &build); err != nil {
		return "", err
	}
	d, ok := build.Downloads[platform]
	if !ok {
		return "", fmt.Errorf("geyser %s-%d: no %s download", build.Version, build.Build, platform)
	}
	u := fmt.Sprintf("%s/%s/builds/%d/downloads/%s", builds, build.Version, build.Build, platform)
	return download(u, fmt.Sprintf("geyser-%s-%d-%s", build.Version, build.Build, d.Name), "sha256", d.SHA256)
}

func fabricLauncherJar(mc string) (string, error) {
	var loaders, installers []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := getJSON("https://meta.fabricmc.net/v2/versions/loader", &loaders); err != nil {
		return "", err
	}
	if err := getJSON("https://meta.fabricmc.net/v2/versions/installer", &installers); err != nil {
		return "", err
	}
	pick := func(list []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}) string {
		for _, x := range list {
			if x.Stable {
				return x.Version
			}
		}
		return ""
	}
	loader, installer := pick(loaders), pick(installers)
	u := fmt.Sprintf("https://meta.fabricmc.net/v2/versions/loader/%s/%s/%s/server/jar", mc, loader, installer)
	// Fabric meta publishes no checksum for the generated launcher.
	return download(u, fmt.Sprintf("fabric-server-%s-%s-%s.jar", mc, loader, installer), "", "")
}

func modrinthJar(slug, mc, loader string) (string, error) {
	q := url.Values{"game_versions": {fmt.Sprintf(`[%q]`, mc)}, "loaders": {fmt.Sprintf(`[%q]`, loader)}}
	var versions []struct {
		VersionNumber string `json:"version_number"`
		Files         []struct {
			URL      string            `json:"url"`
			Filename string            `json:"filename"`
			Primary  bool              `json:"primary"`
			Hashes   map[string]string `json:"hashes"`
		} `json:"files"`
	}
	if err := getJSON("https://api.modrinth.com/v2/project/"+slug+"/version?"+q.Encode(), &versions); err != nil {
		return "", err
	}
	if len(versions) == 0 || len(versions[0].Files) == 0 {
		return "", fmt.Errorf("modrinth %s: no %s build for %s", slug, loader, mc)
	}
	f := versions[0].Files[0]
	for _, x := range versions[0].Files {
		if x.Primary {
			f = x
		}
	}
	return download(f.URL, "mods/"+f.Filename, "sha512", f.Hashes["sha512"])
}

func mavenInstaller(base, group, artifact, version string) (string, error) {
	path := fmt.Sprintf("%s/%s/%s/%s/%s-%s-installer.jar", base, strings.ReplaceAll(group, ".", "/"), artifact, version, artifact, version)
	req, _ := http.NewRequest(http.MethodGet, path+".sha256", nil)
	sum := ""
	if resp, err := httpClient.Do(req); err == nil {
		if resp.StatusCode == 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			sum = strings.Fields(string(b) + " ")[0]
		}
		resp.Body.Close()
	}
	algo := "sha256"
	if sum == "" {
		if resp, err := httpClient.Get(path + ".sha1"); err == nil {
			if resp.StatusCode == 200 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
				sum, algo = strings.Fields(string(b) + " ")[0], "sha1"
			}
			resp.Body.Close()
		}
	}
	return download(path, fmt.Sprintf("%s-%s-installer.jar", artifact, version), algo, sum)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return exec.Command("cp", "-a", src, dst).Run()
}

func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	out, err := exec.Command("cp", "-a", src+"/.", dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	return nil
}

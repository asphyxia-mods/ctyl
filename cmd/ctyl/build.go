package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"ctyl/internal/hygiene"
	"ctyl/internal/ociimage"
	"ctyl/internal/policy"
	"ctyl/internal/render"
	"ctyl/internal/vendor"
)

func loadLine(c commonFlags, major int) (*policy.Policy, policy.Line, error) {
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return nil, policy.Line{}, err
	}
	l, ok := p.Line(major)
	if !ok {
		for _, u := range p.Unsupported {
			if u.Major == major {
				return nil, l, fmt.Errorf("Java %d is not built: %s", major, u.Reason)
			}
		}
		return nil, l, fmt.Errorf("Java %d is not a configured line", major)
	}
	return p, l, nil
}

func cmdLines(args []string) error {
	fs := flag.NewFlagSet("lines", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	all := fs.Bool("all", false, "include lines past their support date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	now, err := c.time()
	if err != nil {
		return err
	}
	type entry struct {
		Major        int    `json:"major"`
		Tag          string `json:"tag"`
		Supported    bool   `json:"supported"`
		SupportUntil string `json:"security_support_until"`
	}
	out := []entry{}
	for _, l := range p.Lines {
		if l.Supported(now) || *all {
			out = append(out, entry{l.Major, l.Tag, l.Supported(now), l.SecuritySupportUntil.Format("2006-01-02")})
		}
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func cmdCandidates(args []string) error {
	fs := flag.NewFlagSet("candidates", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(p.Candidates(), "\n"))
	return nil
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	major := fs.Int("line", 0, "Java major version")
	dist := fs.String("distribution", "", "distribution (default: policy default)")
	vendorPkg := fs.String("vendor-package", "", "local package name for vendor distributions")
	constraints := fs.String("package-constraints", "", "space-separated name=version pins (Wolfi distributions only)")
	resolution := fs.Bool("resolution-only", false, "render a config that resolves only the upstream Wolfi packages (for change detection)")
	out := fs.String("out", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, l, err := loadLine(c, *major)
	if err != nil {
		return err
	}
	if *dist == "" {
		*dist = p.Default
	}
	now, err := c.time()
	if err != nil {
		return err
	}
	var data []byte
	if *resolution {
		data, err = resolutionConfig(c.root, p, l, *dist)
	} else {
		data, err = render.Render(render.Input{Policy: p, Line: l, Distribution: *dist, VendorPackage: *vendorPkg,
			Constraints: strings.Fields(*constraints), Now: now})
	}
	if err != nil {
		return err
	}
	return writeOut(*out, data)
}

// resolutionConfig lists the upstream packages of a Wolfi build without the
// locally built launcher, so change detection can run `apko lock` without
// building anything first.
func resolutionConfig(root string, p *policy.Policy, l policy.Line, dist string) ([]byte, error) {
	d := p.Distributions[dist]
	if d.Kind != "wolfi" {
		return nil, fmt.Errorf("resolution configs are only meaningful for wolfi distributions")
	}
	var base struct {
		Contents struct {
			Packages []string `yaml:"packages"`
		} `yaml:"contents"`
		Archs []string `yaml:"archs"`
	}
	data, err := os.ReadFile(filepath.Join(root, render.BaseConfig))
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &base); err != nil {
		return nil, err
	}
	var pkgs []string
	for _, pkg := range base.Contents.Packages {
		if !strings.Contains(pkg, "@") {
			pkgs = append(pkgs, pkg)
		}
	}
	for _, pkg := range d.Packages {
		pkgs = append(pkgs, policy.Expand(pkg, l))
	}
	pkgs = append(pkgs, l.ExtraPackages...)
	cfg := map[string]any{
		"contents": map[string]any{
			"keyring":      []string{"./keys/wolfi-signing.rsa.pub"},
			"repositories": []string{"https://packages.wolfi.dev/os"},
			"packages":     pkgs,
		},
		"archs": base.Archs,
	}
	return yaml.Marshal(cfg)
}

func writeOut(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeOut(path, append(data, '\n'))
}

func cmdVendorFetch(args []string) error {
	fs := flag.NewFlagSet("vendor-fetch", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	major := fs.Int("line", 0, "Java major version")
	dist := fs.String("distribution", "", "vendor distribution (corretto, temurin)")
	version := fs.String("version", "", "vendor version (default: latest)")
	archs := fs.String("archs", "x86_64,aarch64", "architectures")
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, l, err := loadLine(c, *major)
	if err != nil {
		return err
	}
	d, ok := p.Distributions[*dist]
	if !ok || d.Kind == "wolfi" {
		return fmt.Errorf("%q is not a vendor distribution", *dist)
	}
	f := &vendor.Fetcher{RootDir: c.root, OutDir: *out}
	b, err := f.Fetch(d, l, *version, splitList(*archs))
	if err != nil {
		return err
	}
	var archives []render.VendorArchive
	for _, a := range b.Archives {
		archives = append(archives, render.VendorArchive{Arch: a.Arch, File: a.File, SHA256: a.SHA256, TopDir: a.TopDir})
	}
	recipe, err := render.RenderVendorMelange(render.VendorInput{Line: l, Distribution: d.Name, Version: b.Version, License: b.License, Archives: archives})
	if err != nil {
		return err
	}
	if err := writeOut(filepath.Join(*out, "melange.yaml"), recipe); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "verified %s %s (%s) for %s; package %s\n", d.Name, b.Version, b.Release["JAVA_RUNTIME_VERSION"], *archs, render.VendorPackageName(d.Name, l.Major))
	return writeJSON(filepath.Join(*out, "vendor.json"), b)
}

func cmdHygiene(args []string) error {
	fs := flag.NewFlagSet("hygiene", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	image := fs.String("image", "", "apko image tarball")
	major := fs.Int("line", 0, "Java major version")
	dist := fs.String("distribution", "", "distribution")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, l, err := loadLine(c, *major)
	if err != nil {
		return err
	}
	images, err := ociimage.Open(*image)
	if err != nil {
		return err
	}
	opts := hygiene.Options{Entrypoint: "/usr/bin/ctyl-launch"}
	if p.Distributions[*dist].Kind == "wolfi" {
		opts.DisableExecutables = l.DisableExecutables
	}
	failed := 0
	for _, img := range images {
		v := hygiene.Check(img, opts)
		for _, x := range v {
			fmt.Printf("%s: %s\n", img.Arch, x)
		}
		failed += len(v)
		fmt.Fprintf(os.Stderr, "%s: %d files checked, %d violations\n", img.Arch, len(img.Files()), len(v))
	}
	if failed > 0 {
		return fmt.Errorf("%d hygiene violations", failed)
	}
	return nil
}

// launcherSources are the files that determine the launcher package and the
// shared image layout; they are hashed into the inputs digest.
var launcherSources = []string{"go.mod", "cmd/ctyl-launch", "internal/startup", "internal/launch", "melange/ctyl-launch.yaml", "images/runtime-base.apko.yaml"}

func sourceDigest(root string) (string, error) {
	h := sha256.New()
	for _, src := range launcherSources {
		err := filepath.WalkDir(filepath.Join(root, src), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)+"fuzz") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
			h.Write(data)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type lockFile struct {
	Contents struct {
		Packages []struct {
			Name         string `json:"name"`
			URL          string `json:"url"`
			Version      string `json:"version"`
			Architecture string `json:"architecture"`
			Checksum     string `json:"checksum"`
		} `json:"packages"`
	} `json:"contents"`
}

// upstreamPackages returns "arch/name=version checksum" for packages fetched
// from remote repositories, sorted. Locally built packages are excluded:
// they are represented by the source digest.
func upstreamPackages(lockPath string) ([]string, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	var lf lockFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("%s: %w", lockPath, err)
	}
	var out []string
	for _, p := range lf.Contents.Packages {
		if strings.HasPrefix(p.URL, "https://") {
			out = append(out, fmt.Sprintf("%s/%s=%s %s", p.Architecture, p.Name, p.Version, p.Checksum))
		}
	}
	sort.Strings(out)
	return out, nil
}

func inputsDigest(root, config, lock, vendorJSON string) (string, error) {
	h := sha256.New()
	cfg, err := os.ReadFile(config)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "config\x00%s\x00", cfg)
	pkgs, err := upstreamPackages(lock)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "packages\x00%s\x00", strings.Join(pkgs, "\n"))
	src, err := sourceDigest(root)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "sources\x00%s\x00", src)
	if vendorJSON != "" {
		var b vendor.Build
		data, err := os.ReadFile(vendorJSON)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(data, &b); err != nil {
			return "", err
		}
		for _, a := range b.Archives {
			fmt.Fprintf(h, "vendor\x00%s\x00%s\x00%s\x00", b.Distribution, a.Arch, a.SHA256)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func cmdInputsDigest(args []string) error {
	fs := flag.NewFlagSet("inputs-digest", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	config := fs.String("config", "", "rendered apko config")
	lock := fs.String("lock", "", "apko lock file")
	vendorJSON := fs.String("vendor", "", "vendor.json from vendor-fetch, for vendor builds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := inputsDigest(c.root, *config, *lock, *vendorJSON)
	if err != nil {
		return err
	}
	fmt.Println(d)
	return nil
}

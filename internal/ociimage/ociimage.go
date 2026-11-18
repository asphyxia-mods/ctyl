// Package ociimage reads apko-produced image tarballs without a container
// runtime, so that the exact artifact to be published can be inspected.
package ociimage

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// Image is one platform image inside an apko tarball.
type Image struct {
	Arch   string // OCI architecture (amd64, arm64)
	Tag    string
	Config Config
	tar    string
	layers []string
	files  map[string]*tar.Header
}

type Config struct {
	Architecture string `json:"architecture"`
	Created      string `json:"created"`
	Config       struct {
		Env        []string          `json:"Env"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		User       string            `json:"User"`
		WorkingDir string            `json:"WorkingDir"`
		StopSignal string            `json:"StopSignal"`
		Labels     map[string]string `json:"Labels"`
	} `json:"config"`
}

func (c Config) Env(key string) (string, bool) {
	for _, kv := range c.Config.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// Open reads the manifest of an apko tarball and returns its images.
func Open(tarball string) ([]*Image, error) {
	var manifest []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := readMember(tarball, "manifest.json", func(r io.Reader) error {
		return json.NewDecoder(r).Decode(&manifest)
	}); err != nil {
		return nil, err
	}
	var images []*Image
	for _, m := range manifest {
		img := &Image{tar: tarball, layers: m.Layers}
		if len(m.RepoTags) > 0 {
			img.Tag = m.RepoTags[0]
		}
		if err := readMember(tarball, m.Config, func(r io.Reader) error {
			return json.NewDecoder(r).Decode(&img.Config)
		}); err != nil {
			return nil, err
		}
		img.Arch = img.Config.Architecture
		if err := img.index(); err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("%s: no images in manifest.json", tarball)
	}
	return images, nil
}

// IndexDigest returns the digest a registry reports after `apko publish`:
// the multi-platform index, or the platform manifest itself when the build
// has a single architecture (apko pushes no index then).
func IndexDigest(tarball string) (string, error) {
	var data []byte
	err := readMember(tarball, "index.json", func(r io.Reader) error {
		var err error
		data, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return "", err
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return "", err
	}
	if len(index.Manifests) == 1 {
		return index.Manifests[0].Digest, nil
	}
	return digestOf(data), nil
}

func readMember(tarball, name string, fn func(io.Reader) error) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReader(f))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s: member %s not found", tarball, name)
		}
		if err != nil {
			return err
		}
		if h.Name == name {
			return fn(tr)
		}
	}
}

func (img *Image) eachLayerEntry(fn func(h *tar.Header, r io.Reader) (stop bool, err error)) error {
	for _, layer := range img.layers {
		var stop bool
		err := readMember(img.tar, layer, func(r io.Reader) error {
			gz, err := gzip.NewReader(r)
			if err != nil {
				return err
			}
			defer gz.Close()
			tr := tar.NewReader(gz)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				if stop, err = fn(h, tr); err != nil || stop {
					return err
				}
			}
		})
		if err != nil || stop {
			return err
		}
	}
	return nil
}

func clean(p string) string {
	return path.Clean("/" + strings.TrimPrefix(p, "./"))
}

func (img *Image) index() error {
	img.files = map[string]*tar.Header{}
	return img.eachLayerEntry(func(h *tar.Header, _ io.Reader) (bool, error) {
		img.files[clean(h.Name)] = h
		return false, nil
	})
}

// Files returns every path in the image, sorted.
func (img *Image) Files() []string {
	out := make([]string, 0, len(img.files))
	for p := range img.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (img *Image) Header(p string) (*tar.Header, bool) {
	h, ok := img.files[clean(p)]
	return h, ok
}

// Resolve follows symlinks (in every path component) within the image.
func (img *Image) Resolve(p string) (string, error) {
	p = clean(p)
	for hops := 0; hops < 40; hops++ {
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		cur := "/"
		changed := false
		for i, part := range parts {
			next := path.Join(cur, part)
			h, ok := img.files[next]
			if ok && h.Typeflag == tar.TypeSymlink {
				target := h.Linkname
				if !strings.HasPrefix(target, "/") {
					target = path.Join(cur, target)
				}
				p = clean(path.Join(append([]string{target}, parts[i+1:]...)...))
				changed = true
				break
			}
			cur = next
		}
		if !changed {
			return p, nil
		}
	}
	return "", fmt.Errorf("too many symlinks resolving %s", p)
}

// ReadFile returns the content of a regular file, following symlinks.
func (img *Image) ReadFile(p string) ([]byte, error) {
	resolved, err := img.Resolve(p)
	if err != nil {
		return nil, err
	}
	h, ok := img.files[resolved]
	if !ok {
		return nil, fmt.Errorf("%s: not found in image", p)
	}
	if h.Typeflag == tar.TypeLink {
		resolved = clean(h.Linkname)
	}
	var data []byte
	found := false
	err = img.eachLayerEntry(func(h *tar.Header, r io.Reader) (bool, error) {
		if clean(h.Name) != resolved || h.Typeflag != tar.TypeReg {
			return false, nil
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(r, 64<<20))
		found = true
		return false, err
	})
	if err == nil && !found {
		err = fmt.Errorf("%s: not a regular file", p)
	}
	return data, err
}

// Package is an entry of the apk installed database.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Origin  string `json:"origin,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// Packages parses /usr/lib/apk/db/installed.
func (img *Image) Packages() ([]Package, error) {
	data, err := img.ReadFile("/usr/lib/apk/db/installed")
	if err != nil {
		data, err = img.ReadFile("/lib/apk/db/installed")
	}
	if err != nil {
		return nil, err
	}
	var out []Package
	var cur Package
	flush := func() {
		if cur.Name != "" {
			out = append(out, cur)
		}
		cur = Package{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "P":
			cur.Name = v
		case "V":
			cur.Version = v
		case "o":
			cur.Origin = v
		case "c":
			cur.Commit = v
		}
	}
	flush()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

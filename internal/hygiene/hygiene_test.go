package hygiene

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctyl/internal/ociimage"
)

type entry struct {
	name     string
	typ      byte
	mode     int64
	uid      int
	body     []byte
	linkname string
}

// buildImage writes an apko-style tarball (manifest.json, config, one layer).
func buildImage(t *testing.T, entries []entry, user string) string {
	t.Helper()
	var layer bytes.Buffer
	gz := gzip.NewWriter(&layer)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Uid: e.uid, Size: int64(len(e.body)), Linkname: e.linkname}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		tw.WriteHeader(h)
		if e.typ == tar.TypeReg {
			tw.Write(e.body)
		}
	}
	tw.Close()
	gz.Close()
	cfg, _ := json.Marshal(map[string]any{"architecture": "amd64", "config": map[string]any{
		"Entrypoint": []string{"/usr/bin/ctyl-launch"}, "User": user, "WorkingDir": "/home/container"}})
	manifest, _ := json.Marshal([]map[string]any{{"Config": "sha256:cfg", "RepoTags": []string{"t:amd64"}, "Layers": []string{"layer.tar.gz"}}})

	path := filepath.Join(t.TempDir(), "image.tar")
	f, _ := os.Create(path)
	out := tar.NewWriter(f)
	for name, data := range map[string][]byte{"manifest.json": manifest, "sha256:cfg": cfg, "layer.tar.gz": layer.Bytes(), "index.json": []byte(`{"manifests":[]}`)} {
		out.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		out.Write(data)
	}
	out.Close()
	f.Close()
	return path
}

func staticELF(t *testing.T) []byte {
	exe, err := os.Executable() // the test binary is not static, which is what the negative case needs
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(exe)
	return data
}

func check(t *testing.T, entries []entry, user string, opts Options) []string {
	t.Helper()
	imgs, err := ociimage.Open(buildImage(t, entries, user))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range Check(imgs[0], opts) {
		out = append(out, v.String())
	}
	return out
}

func TestHygieneFindsRegressions(t *testing.T) {
	entries := []entry{
		{name: "bin", typ: tar.TypeSymlink, linkname: "usr/bin"},
		{name: "usr/bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "usr/bin/busybox", typ: tar.TypeReg, mode: 0o755, body: []byte("x")},
		{name: "usr/bin/sh", typ: tar.TypeSymlink, linkname: "busybox"},
		{name: "usr/bin/su", typ: tar.TypeReg, mode: 0o4755, body: []byte("x")},
		{name: "usr/bin/ctyl-launch", typ: tar.TypeReg, mode: 0o755, body: staticELF(t)},
		{name: "usr/lib/jvm/x/bin/rmiregistry", typ: tar.TypeReg, mode: 0o755, body: []byte("x")},
		{name: "etc/passwd", typ: tar.TypeReg, mode: 0o644, body: []byte("root:x:0:0")},
		{name: "srv/", typ: tar.TypeDir, mode: 0o777},
		{name: "tmp/", typ: tar.TypeDir, mode: 0o777},
		{name: "opt/data", typ: tar.TypeReg, mode: 0o644, uid: RuntimeUID, body: []byte("x")},
		{name: "home/container/", typ: tar.TypeDir, mode: 0o755, uid: RuntimeUID},
		{name: "dev/null", typ: tar.TypeChar, mode: 0o666},
	}
	got := strings.Join(check(t, entries, "0", Options{Entrypoint: "/usr/bin/ctyl-launch", DisableExecutables: []string{"/usr/lib/jvm/x/bin/rmiregistry"}}), "\n")
	for _, want := range []string{
		`/usr/bin/busybox: forbidden executable "busybox"`,
		`/usr/bin/sh: forbidden executable "sh"`,
		`/usr/bin/su: setuid/setgid`,
		`/srv: world-writable`,
		`/tmp: world-writable directory without sticky bit`,
		`/opt/data: writable by the runtime user`,
		`rmiregistry: should be disabled`,
		`image user "0" is root`,
		`/usr/bin/ctyl-launch: dynamically linked`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing violation %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"/etc/passwd", "/dev/null", "/home/container"} {
		if strings.Contains(got, unwanted+":") {
			t.Errorf("false positive for %s:\n%s", unwanted, got)
		}
	}
}

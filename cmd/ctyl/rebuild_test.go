package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"ctyl/internal/evaluate"
)

func TestRebuildReasons(t *testing.T) {
	base := State{Digest: "sha256:old", Distribution: "wolfi", DistributionVersion: "21-r0", RuntimeVersion: "21+wolfi-r0", SourceCommit: "abc", Revision: "image1",
		Packages: map[string][]string{"x86_64": {"glibc=1", "openjdk-21-jre=21-r0", "ctyl-launch=1"}}}
	tests := []struct {
		name      string
		change    func(*State)
		want      string
		unchanged bool
	}{
		{"dependency", func(s *State) {
			s.Packages = map[string][]string{"x86_64": {"ctyl-launch=1", "glibc=2", "openjdk-21-jre=21-r0"}}
		}, "Wolfi dependencies updated", true},
		{"java package revision", func(s *State) { s.DistributionVersion = "21-r1"; s.RuntimeVersion = "21+wolfi-r1" }, "wolfi Java package updated", true},
		{"java source", func(s *State) { s.SourceCommit = "def" }, "Java runtime/source updated", false},
		{"image source", func(s *State) { s.Revision = "image2" }, "image/launcher sources updated", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := base
			next.Digest = "sha256:new"
			tt.change(&next)
			got := rebuildReasons(base, next)
			if !slices.Contains(got, tt.want) || slices.Contains(got, "Java runtime/source unchanged") != tt.unchanged {
				t.Fatalf("got %v", got)
			}
		})
	}
	reordered := base
	reordered.Packages = map[string][]string{"x86_64": {"ctyl-launch=1", "openjdk-21-jre=21-r0", "glibc=1"}}
	if packageVersions(base, false) != packageVersions(reordered, false) {
		t.Fatal("package ordering changed comparison")
	}
}

func TestMetadataRetainsPublicationOnIdenticalRebuild(t *testing.T) {
	dir := t.TempDir()
	previous := State{Digest: "sha256:same", BuiltAt: "2026-11-17T10:00:00Z", PublishedAt: "2026-11-17T11:00:00Z", RunURL: "original-run", RebuildReasons: []string{"Wolfi dependencies updated"}}
	gate := GateResult{IndexDigest: previous.Digest, EvaluatedAt: time.Now(), Reports: []evaluate.Report{{JavaReleaseDate: "2026-11-17"}}}
	for name, value := range map[string]any{"previous.json": previous, "gate.json": gate} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "out.json")
	if err := cmdMetadata([]string{"--report", filepath.Join(dir, "gate.json"), "--previous", filepath.Join(dir, "previous.json"), "--digest", previous.Digest, "--out", out}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.JavaReleaseDate != "2026-11-17" {
		t.Fatalf("release date lost: %q", got.JavaReleaseDate)
	}
	if got.BuiltAt != previous.BuiltAt || got.PublishedAt != previous.PublishedAt || got.RunURL != previous.RunURL || !slices.Equal(got.RebuildReasons, previous.RebuildReasons) {
		t.Fatalf("publication changed: %+v", got)
	}
}

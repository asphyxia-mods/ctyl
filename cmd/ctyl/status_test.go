package main

import (
	"path/filepath"
	"strings"
	"testing"

	"ctyl/internal/policy"
)

func TestReadmeStatusSection(t *testing.T) {
	p, err := policy.Load(filepath.Join("../..", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	states := []State{{Line: 21, Tag: "java_21", Digest: "sha256:7d013666d7ddcd7a335d14b5456068ab991eb72c66571cf1319e44f26b8a2b7a",
		Distribution: "wolfi", DistributionVersion: "21.0.13.1-r0", RuntimeVersion: "21.0.13.1+-wolfi-r0", Status: "verified",
		Promoted: true, BuiltAt: "2026-11-18T11:45:23Z", PublishedAt: "2026-11-18T11:57:43Z", CheckedAt: "2026-11-18T11:57:43Z",
		RunURL: "https://github.com/o/r/actions/runs/1"}}
	section := readmeStatus(p, states, "ghcr.io/o/ctyl")
	for _, want := range []string{
		"| `ghcr.io/o/ctyl:java_21` | 21.0.13.1 (wolfi 21.0.13.1-r0) | ✅ verified | 2026-11-18 11:45 UTC | [2026-11-18 11:57 UTC](https://github.com/o/r/actions/runs/1) |",
		"ghcr.io/o/ctyl:java_21@sha256:7d013666d7ddcd7a335d14b5456068ab991eb72c66571cf1319e44f26b8a2b7a",
		"| `ghcr.io/o/ctyl:java_8` | 8 | not published yet |",
		"Last publication: 2026-11-18 11:57 UTC",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("missing %q in:\n%s", want, section)
		}
	}
	doc := "intro\n" + statusBegin + "\nold\n" + statusEnd + "\nrest\n"
	got, err := replaceSection(doc, section)
	if err != nil || !strings.HasPrefix(got, "intro\n"+statusBegin+"\n_Generated") || !strings.HasSuffix(got, statusEnd+"\nrest\n") || strings.Contains(got, "old") {
		t.Errorf("replaceSection: %v\n%s", err, got)
	}
	again, _ := replaceSection(got, section)
	if again != got {
		t.Error("regenerating an unchanged state is not idempotent")
	}
	if _, err := replaceSection("no markers", section); err == nil {
		t.Error("missing markers accepted")
	}
}

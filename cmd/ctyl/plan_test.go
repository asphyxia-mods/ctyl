package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctyl/internal/gitcache"
	"ctyl/internal/policy"
)

func writeLock(t *testing.T, dir, tag, version string) {
	t.Helper()
	lock := map[string]any{"contents": map[string]any{"packages": []map[string]string{
		{"name": "openjdk-21-jre", "url": "https://packages.wolfi.dev/os/x86_64/openjdk-21-jre-" + version + ".apk", "version": version, "architecture": "x86_64", "checksum": "Q1" + version},
		{"name": "ctyl-launch", "url": "./build/packages/x86_64/ctyl-launch-0.1.0-r0.apk", "version": "0.1.0-r0", "architecture": "x86_64", "checksum": "Q1local"},
	}}}
	data, _ := json.Marshal(lock)
	os.WriteFile(filepath.Join(dir, tag+".lock.json"), data, 0o644)
	os.WriteFile(filepath.Join(dir, tag+".apko.yaml"), []byte("include: images/runtime-base.apko.yaml\n"), 0o644)
}

// TestPlanDetectsNewWolfiPackages checks that a fresh resolution with a newer
// package triggers a rebuild while an identical one does not.
func TestPlanDetectsNewWolfiPackages(t *testing.T) {
	root := "../.."
	p, err := policy.Load(filepath.Join(root, "policy"))
	if err != nil {
		t.Fatal(err)
	}
	l, _ := p.Line(21)
	locks, state := t.TempDir(), t.TempDir()
	writeLock(t, locks, l.Tag, "21.0.13.1-r0")
	digest, err := inputsDigest(root, filepath.Join(locks, l.Tag+".apko.yaml"), filepath.Join(locks, l.Tag+".lock.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(State{Tag: l.Tag, InputsDigest: digest, PolicyDigest: p.Digest, Status: "verified"})
	os.WriteFile(filepath.Join(state, l.Tag+".json"), st, 0o644)

	cache := gitcache.New(t.TempDir())
	cache.Offline = true
	c := commonFlags{root: root}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	e, _ := planLine(c, p, l, cache, state, locks, "", false, now)
	if len(e.Reasons) != 0 {
		t.Fatalf("unchanged inputs triggered a rebuild: %v", e.Reasons)
	}

	writeLock(t, locks, l.Tag, "21.0.13.2-r0")
	e, _ = planLine(c, p, l, cache, state, locks, "", false, now)
	if len(e.Reasons) == 0 || !strings.Contains(strings.Join(e.Reasons, " "), "Wolfi resolution") {
		t.Fatalf("new Wolfi package not detected: %v", e.Reasons)
	}

	// Locally built package checksums must not cause rebuild loops.
	writeLock(t, locks, l.Tag, "21.0.13.1-r0")
	data, _ := os.ReadFile(filepath.Join(locks, l.Tag+".lock.json"))
	os.WriteFile(filepath.Join(locks, l.Tag+".lock.json"), []byte(strings.Replace(string(data), "Q1local", "Q1other", 1)), 0o644)
	if e, _ := planLine(c, p, l, cache, state, locks, "", false, now); len(e.Reasons) != 0 {
		t.Fatalf("local package checksum change triggered a rebuild: %v", e.Reasons)
	}
}

func TestPlanRetriesNonVerifiedAndPolicyChanges(t *testing.T) {
	root := "../.."
	p, _ := policy.Load(filepath.Join(root, "policy"))
	l, _ := p.Line(17)
	state := t.TempDir()
	st, _ := json.Marshal(State{Tag: l.Tag, PolicyDigest: "sha256:old", Status: "vulnerable"})
	os.WriteFile(filepath.Join(state, l.Tag+".json"), st, 0o644)
	cache := gitcache.New(t.TempDir())
	cache.Offline = true
	e, _ := planLine(commonFlags{root: root}, p, l, cache, state, "", "", false, time.Now())
	joined := strings.Join(e.Reasons, " ")
	if !strings.Contains(joined, "policy changed") || !strings.Contains(joined, "vulnerable") {
		t.Errorf("reasons %v", e.Reasons)
	}
}

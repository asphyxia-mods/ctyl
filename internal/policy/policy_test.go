package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func copyPolicy(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	filepath.Walk("../../policy", func(p string, info os.FileInfo, err error) error {
		rel, _ := filepath.Rel("../../policy", p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	return dst
}

func TestRepositoryPolicyIsValid(t *testing.T) {
	p, err := Load("../../policy")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) == 0 || len(p.Advisories) == 0 || !strings.HasPrefix(p.Digest, "sha256:") {
		t.Fatalf("unexpected policy %+v", p)
	}
}

func TestInvalidPoliciesRejected(t *testing.T) {
	cases := map[string]string{
		"override without evidence": "kind: baseline\nline: 17\nrequire_commit: 8ea73158afc9f2c62f72df68beb8723032bf695e\nreason: x\napproved_by: me\nexpires: 2030-01-01\n",
		"override without expiry":   "kind: baseline\nline: 17\nrequire_commit: 8ea73158afc9f2c62f72df68beb8723032bf695e\nreason: x\napproved_by: me\nevidence: [https://x]\n",
		"abbreviated commit":        "kind: baseline\nline: 17\nrequire_commit: 8ea73158\nreason: x\napproved_by: me\nevidence: [https://x]\nexpires: 2030-01-01\n",
		"unknown field":             "kind: baseline\nline: 17\nrequire_commit: 8ea73158afc9f2c62f72df68beb8723032bf695e\nreason: x\napproved_by: me\nevidence: [https://x]\nexpires: 2030-01-01\nskip_gate: true\n",
		"unknown line":              "kind: verify\nadvisory: CVE-1\nline: 26\ndistribution: wolfi\nruntime_version: x\nreason: x\napproved_by: me\nevidence: [https://x]\nexpires: 2030-01-01\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyPolicy(t)
			os.WriteFile(filepath.Join(dir, "overrides", "bad.yaml"), []byte(body), 0o644)
			if _, err := Load(dir); err == nil {
				t.Error("invalid override accepted")
			}
		})
	}
}

func TestPolicyDigestChangesWithAdvisories(t *testing.T) {
	dir := copyPolicy(t)
	before, _ := Load(dir)
	os.WriteFile(filepath.Join(dir, "advisories", "CVE-2099-0001.yaml"), []byte("id: CVE-2099-0001\nseverity: high\nlines:\n  \"21\":\n    status: affected\n"), 0o644)
	after, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest == after.Digest {
		t.Error("policy digest unchanged after adding an advisory")
	}
}

func TestOverrideExpiry(t *testing.T) {
	o := Override{Expires: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}
	if !o.Active(time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)) || o.Active(time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)) {
		t.Error("expiry is inclusive of the expiry date only")
	}
}

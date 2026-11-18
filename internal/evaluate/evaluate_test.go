package evaluate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctyl/internal/gitcache"
	"ctyl/internal/launch"
	"ctyl/internal/policy"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func loadPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.Load("../../policy")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func release(t *testing.T, name string) map[string]string {
	t.Helper()
	rel, err := launch.ReadRelease(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func line(t *testing.T, p *policy.Policy, major int) policy.Line {
	t.Helper()
	l, ok := p.Line(major)
	if !ok {
		t.Fatalf("line %d missing", major)
	}
	return l
}

// fakeGraph is a commit graph: commit -> parents; repos list known commits.
type fakeGraph struct {
	parents map[string][]string
	repos   map[string][]string
}

func (g fakeGraph) Locate(repos []string, rev string) (string, string, error) {
	for _, r := range repos {
		for _, c := range g.repos[r] {
			if strings.HasPrefix(c, rev) {
				return r, c, nil
			}
		}
	}
	return "", "", nil
}

func (g fakeGraph) IsAncestor(repo, a, d string) (bool, error) {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(c string) bool {
		if strings.HasPrefix(c, a) || strings.HasPrefix(a, c) {
			return true
		}
		if seen[c] {
			return false
		}
		seen[c] = true
		for _, p := range g.parents[c] {
			if walk(p) {
				return true
			}
		}
		return false
	}
	return walk(d), nil
}

const (
	fix17     = "8ea73158afc9f2c62f72df68beb8723032bf695e"
	ga1701    = "10ebaa9a96d18973cfd08ec6828965886945f14f" // jdk-17.0.21.1-ga
	ga170003  = "b84391a2df6cf9fff72c1580502dd7b2b1838dd6" // jdk-17.0.21.0.3-ga
	upstream7 = "https://github.com/openjdk/jdk17u"
)

func graph() fakeGraph {
	return fakeGraph{
		parents: map[string][]string{
			ga1701: {fix17},
			fix17:  {ga170003},
		},
		repos: map[string][]string{upstream7: {ga1701, fix17, ga170003}},
	}
}

func find(r Report, prefix string) *Finding {
	for i := range r.Findings {
		if strings.HasPrefix(r.Findings[i].Advisory, prefix) {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestPatchedWolfiBuildPasses(t *testing.T) {
	p := loadPolicy(t)
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.1-r0.release"), DistVersion: "17.0.21.1-r0"}, graph(), now)
	if !r.Promotable || r.Status != StatusVerified {
		t.Fatalf("patched build rejected: %+v", r)
	}
	f := find(r, "CVE-2026-114938")
	if f == nil || f.Status != FindingFixed || !strings.Contains(strings.Join(f.Evidence, " "), "contains fix 8ea73158afc9") {
		t.Errorf("finding %+v", f)
	}
}

func TestVulnerableBuildIsBlockedEvenWithNewerLookingVersion(t *testing.T) {
	p := loadPolicy(t)
	rel := release(t, "wolfi-17.0.21.0.3-r1.release")
	// The version string must not matter: claim a "fixed" version, keep the old source.
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: rel, DistVersion: "17.0.99-r0"}, graph(), now)
	if r.Promotable || r.Status != StatusVulnerable {
		t.Fatalf("vulnerable build passed: %+v", r)
	}
	if f := find(r, "CVE-2026-114938"); f == nil || !f.Blocking || f.Status != FindingVulnerable {
		t.Errorf("finding %+v", f)
	}
}

func TestUnknownSourceAwaitsVerification(t *testing.T) {
	p := loadPolicy(t)
	rel := release(t, "wolfi-17.0.21.1-r0.release")
	rel["SOURCE"] = ".:git:0123456789ab"
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: rel}, graph(), now)
	if r.Promotable || r.Status != StatusAwaiting {
		t.Fatalf("unverifiable build: %+v", r)
	}
}

func TestMaliciousSourceFieldIsIgnored(t *testing.T) {
	p := loadPolicy(t)
	rel := release(t, "wolfi-17.0.21.1-r0.release")
	rel["SOURCE"] = ".:git:--upload-pack=touch /tmp/pwned"
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: rel}, graph(), now)
	if r.SourceCommit != "" || r.Promotable {
		t.Fatalf("malicious SOURCE used: %+v", r)
	}
}

func TestVendorVersionEvidence(t *testing.T) {
	p := loadPolicy(t)
	rel := release(t, "corretto-17.0.21.1.2.release")
	delete(rel, "SOURCE")
	ok := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "corretto", Release: rel, DistVersion: "17.0.21.1.2"}, graph(), now)
	if !ok.Promotable {
		t.Errorf("fixed corretto version rejected: %+v", ok)
	}
	old := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "corretto", Release: rel, DistVersion: "17.0.21.0.11"}, graph(), now)
	if old.Promotable || old.Status != StatusVulnerable {
		t.Errorf("old corretto version accepted: %+v", old)
	}
}

func TestVendorForkWithoutUpstreamFixCommitIsNotConfirmedVulnerable(t *testing.T) {
	p := loadPolicy(t)
	g := graph()
	fork := "https://github.com/corretto/corretto-17"
	g.repos[fork] = []string{"9ba6259ac5e180786de3e074b6ff7d0f09b0ba54"}
	// Fork commit with no path to the upstream fix (cherry-picked under another hash).
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "corretto", Release: release(t, "corretto-17.0.21.1.2.release")}, g, now)
	f := find(r, "CVE-2026-114938")
	if f.Status != FindingAwaiting || r.Promotable {
		t.Errorf("want awaiting-verification without vendor version evidence, got %+v", f)
	}
}

func TestNotAffectedLine(t *testing.T) {
	p := loadPolicy(t)
	r := Evaluate(p, Candidate{Line: line(t, p, 11), Distribution: "wolfi", Release: release(t, "wolfi-11.0.33.1-r0.release")}, graph(), now)
	f := find(r, "CVE-2026-114938")
	if f == nil || f.Status != FindingNotAffected || !r.Promotable {
		t.Errorf("Java 11 should be not-affected and promotable: %+v", r)
	}
}

func TestUnsupportedLineNeverPromotes(t *testing.T) {
	p := loadPolicy(t)
	l := line(t, p, 27)
	r := Evaluate(p, Candidate{Line: l, Distribution: "wolfi", Release: map[string]string{}}, graph(), l.SecuritySupportUntil.AddDate(0, 0, 2))
	if r.Status != StatusUnsupported || r.Promotable {
		t.Errorf("EOL line promotable: %+v", r)
	}
}

func withOverride(t *testing.T, p *policy.Policy, o policy.Override) *policy.Policy {
	t.Helper()
	cp := *p
	o.File = "test-override.yaml"
	o.Reason, o.ApprovedBy, o.Evidence = "test", "@test", []string{"https://example.invalid"}
	if o.Expires.IsZero() {
		o.Expires = now.AddDate(0, 1, 0)
	}
	cp.Overrides = append(append([]policy.Override(nil), p.Overrides...), o)
	return &cp
}

func TestBaselineOverride(t *testing.T) {
	p := withOverride(t, loadPolicy(t), policy.Override{Kind: policy.OverrideBaseline, Line: 17, RequireCommit: fix17})
	good := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.1-r0.release")}, graph(), now)
	if !good.Promotable {
		t.Errorf("baseline-satisfying build rejected: %+v", good.Reasons)
	}
	// A baseline must block even when no advisory covers the line.
	p.Advisories = nil
	bad := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.0.3-r1.release")}, graph(), now)
	if bad.Promotable {
		t.Errorf("baseline-violating build accepted: %+v", bad)
	}
}

func TestExpiredOverrideIgnored(t *testing.T) {
	p := withOverride(t, loadPolicy(t), policy.Override{Kind: policy.OverrideVerify, Advisory: "CVE-2026-114938", Line: 17,
		Distribution: "wolfi", RuntimeVersion: "17.0.21.0.3+0-wolfi-r0", Expires: now.AddDate(0, 0, -2)})
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.0.3-r1.release")}, graph(), now)
	if r.Promotable || len(r.Warnings) == 0 {
		t.Errorf("expired override honoured: %+v", r)
	}
}

func TestVerifyOverride(t *testing.T) {
	p := withOverride(t, loadPolicy(t), policy.Override{Kind: policy.OverrideVerify, Advisory: "CVE-2026-114938", Line: 17,
		Distribution: "wolfi", RuntimeVersion: "17.0.21.0.3+0-wolfi-r0"})
	r := Evaluate(p, Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.0.3-r1.release")}, graph(), now)
	if !r.Promotable {
		t.Errorf("reviewed verification ignored: %+v", r)
	}
}

func TestAcceptMitigatedRequiresMitigationInImage(t *testing.T) {
	p := withOverride(t, loadPolicy(t), policy.Override{Kind: policy.OverrideAcceptMitigated, Advisory: "CVE-2026-114938", Line: 17})
	c := Candidate{Line: line(t, p, 17), Distribution: "wolfi", Release: release(t, "wolfi-17.0.21.0.3-r1.release")}
	if r := Evaluate(p, c, graph(), now); r.Promotable {
		t.Error("accepted without the mitigation in the image")
	}
	c.JDKJavaOptions = "-XX:-UseBASE64Intrinsics"
	r := Evaluate(p, c, graph(), now)
	if !r.Promotable || r.Status != StatusVulnerable {
		t.Errorf("mitigated build must be promotable but still reported vulnerable: %+v", r)
	}
	if f := find(r, "CVE-2026-114938"); !f.Mitigated || !strings.Contains(strings.Join(f.Evidence, " "), "NOT FIXED") {
		t.Errorf("mitigation not clearly reported: %+v", f)
	}
	if got := p.ActiveMitigations(17, now); len(got) != 1 || got[0] != "-XX:-UseBASE64Intrinsics" {
		t.Errorf("ActiveMitigations = %q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"17.0.21.1.2", "17.0.21.1.2", 0},
		{"17.0.21.1.2", "17.0.21.0.11", 1},
		{"17.0.21.1-r0", "17.0.21.0.3-r1", 1},
		{"17.0.21", "17.0.21.1", -1},
		{"17.0.21.0.3-r1", "17.0.21.1-r0", -1},
		{"8.514-r0", "8.512-r1", 1},
		{"17.0.21.1+1", "17.0.21+9", 1},
		{"17.0.21+9", "17.0.21+10", -1},
		{"17.0.21", "17.0.21.0", 0},
		{"21.0.13+9-LTS", "21.0.13+9", 0},
	}
	for _, tt := range tests {
		if got := CompareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareVersions(%s, %s) = %d want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestLatestGA(t *testing.T) {
	tags := map[string]string{"jdk-17.0.21-ga": "a", "jdk-17.0.21.0.3-ga": "b", "jdk-17.0.21.1-ga": "c", "jdk-17.0.9-ga": "d", "jdk-17.0.21.1+2": "e"}
	name, commit := LatestGA(tags, `^jdk-(17(?:\.\d+)*)-ga$`)
	if name != "jdk-17.0.21.1-ga" || commit != "c" {
		t.Errorf("got %s %s", name, commit)
	}
	name, _ = LatestGA(map[string]string{"jdk8u512-ga": "x", "jdk8u514-ga": "y", "jdk8u99-ga": "z"}, `^jdk8u(\d+)-ga$`)
	if name != "jdk8u514-ga" {
		t.Errorf("8u: %s", name)
	}
}

// TestRealUpstreamEvidence checks the regression scenario against the real
// OpenJDK, Corretto and Adoptium repositories. It needs network access.
func TestRealUpstreamEvidence(t *testing.T) {
	if os.Getenv("CTYL_NETWORK_TESTS") != "1" {
		t.Skip("set CTYL_NETWORK_TESTS=1 to run against real git repositories")
	}
	p := loadPolicy(t)
	cache := gitcache.New(filepath.Join(os.TempDir(), "ctyl-gitcache-test"))
	l := line(t, p, 17)
	tags, err := cache.Tags(l.UpstreamRepo)
	if err != nil {
		t.Fatal(err)
	}
	ga, gaCommit := LatestGA(tags, l.UpstreamTagPattern)
	tests := []struct {
		dist, file, distVersion string
		promotable              bool
		status                  string
	}{
		{"wolfi", "wolfi-17.0.21.1-r0.release", "17.0.21.1-r0", true, StatusVerified},
		{"wolfi", "wolfi-17.0.21.0.3-r1.release", "17.0.21.0.3-r1", false, StatusVulnerable},
		{"corretto", "corretto-17.0.21.1.2.release", "17.0.21.1.2", true, StatusVerified},
		{"temurin", "temurin-17.0.21+9.release", "17.0.21+9", false, StatusVulnerable},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			r := Evaluate(p, Candidate{Line: l, Distribution: tt.dist, Release: release(t, tt.file), DistVersion: tt.distVersion,
				UpstreamGA: ga, UpstreamGACommit: gaCommit}, cache, now)
			if r.Promotable != tt.promotable || r.Status != tt.status {
				t.Errorf("promotable=%v status=%s, want %v %s\nfindings: %+v\nwarnings: %v", r.Promotable, r.Status, tt.promotable, tt.status, r.Findings, r.Warnings)
			}
		})
	}
}

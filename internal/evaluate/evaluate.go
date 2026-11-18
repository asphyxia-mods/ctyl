// Package evaluate decides the security status of a concrete JDK build
// against the policy, using evidence that does not depend on version strings
// wherever possible.
package evaluate

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctyl/internal/gitcache"
	"ctyl/internal/policy"
)

// Line and finding statuses.
const (
	StatusVerified    = "verified"              // every applicable advisory verified fixed or not affected
	StatusAwaiting    = "awaiting-verification" // potentially affected, no conclusive evidence yet
	StatusVulnerable  = "vulnerable"            // evidence shows a required fix is missing
	StatusUnsupported = "unsupported"           // line outside its security support window

	FindingFixed       = "fixed"
	FindingNotAffected = "not-affected"
	FindingVulnerable  = "vulnerable"
	FindingAwaiting    = "awaiting-verification"
)

type Ancestry interface {
	Locate(repos []string, rev string) (repo, full string, err error)
	IsAncestor(repo, ancestor, descendant string) (bool, error)
}

type Candidate struct {
	Line         policy.Line
	Distribution string
	Arch         string
	Release      map[string]string
	// DistVersion is the distribution's own version for the build (apk
	// version for Wolfi, vendor version for archives).
	DistVersion string
	// JDKJavaOptions is the JDK_JAVA_OPTIONS baked into the image, if any.
	JDKJavaOptions string
	// UpstreamGA is the newest GA tag of the line's upstream repository.
	UpstreamGA, UpstreamGACommit string
}

type Finding struct {
	Advisory  string   `json:"advisory"`
	Severity  string   `json:"severity"`
	Status    string   `json:"status"`
	Blocking  bool     `json:"blocking"`
	Mitigated bool     `json:"mitigated,omitempty"`
	Evidence  []string `json:"evidence"`
}

type Report struct {
	Line           int       `json:"line"`
	Tag            string    `json:"tag"`
	Arch           string    `json:"arch"`
	Distribution   string    `json:"distribution"`
	DistVersion    string    `json:"distribution_version"`
	RuntimeVersion string    `json:"runtime_version"`
	Implementor    string    `json:"implementor"`
	SourceCommit   string    `json:"source_commit,omitempty"`
	SourceRepo     string    `json:"source_repo,omitempty"`
	SourceDirty    bool      `json:"source_dirty,omitempty"`
	SupportUntil   string    `json:"security_support_until"`
	Status         string    `json:"status"`
	Promotable     bool      `json:"promotable"`
	Reasons        []string  `json:"reasons,omitempty"`
	Findings       []Finding `json:"findings"`
	UpstreamGA     string    `json:"upstream_latest_ga,omitempty"`
	BehindUpstream *bool     `json:"behind_upstream,omitempty"`
	Warnings       []string  `json:"warnings,omitempty"`
	PolicyDigest   string    `json:"policy_digest"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

var sourceRE = regexp.MustCompile(`^\.:(?:git|hg):([0-9a-f]{7,40})(\+?)$`)

// SourceCommit extracts the root repository commit from a release file's
// SOURCE field, e.g. ".:git:10ebaa9a96d1+" (the "+" marks local changes).
func SourceCommit(release map[string]string) (commit string, dirty bool, ok bool) {
	for _, entry := range strings.Fields(release["SOURCE"]) {
		if m := sourceRE.FindStringSubmatch(entry); m != nil {
			return m[1], m[2] == "+", true
		}
	}
	return "", false, false
}

// Evaluate produces the gate report for one platform build.
func Evaluate(p *policy.Policy, c Candidate, anc Ancestry, now time.Time) Report {
	r := Report{
		Line:           c.Line.Major,
		Tag:            c.Line.Tag,
		Arch:           c.Arch,
		Distribution:   c.Distribution,
		DistVersion:    c.DistVersion,
		RuntimeVersion: firstNonEmpty(c.Release["JAVA_RUNTIME_VERSION"], c.Release["JAVA_VERSION"]),
		Implementor:    strings.TrimSpace(c.Release["IMPLEMENTOR"] + " " + c.Release["IMPLEMENTOR_VERSION"]),
		SupportUntil:   c.Line.SecuritySupportUntil.Format("2006-01-02"),
		PolicyDigest:   p.Digest,
		EvaluatedAt:    now.UTC(),
		UpstreamGA:     c.UpstreamGA,
		Findings:       []Finding{},
	}
	dist := p.Distributions[c.Distribution]

	src := sourceInfo{}
	if commit, dirty, ok := SourceCommit(c.Release); ok {
		r.SourceCommit, r.SourceDirty = commit, dirty
		repo, full, err := anc.Locate(dist.ReposFor(c.Line), commit)
		switch {
		case err != nil:
			r.Warnings = append(r.Warnings, fmt.Sprintf("could not resolve source commit %s: %v", commit, err))
		case repo == "":
			r.Warnings = append(r.Warnings, fmt.Sprintf("source commit %s not found in %v", commit, dist.ReposFor(c.Line)))
		default:
			src = sourceInfo{repo: repo, commit: full, dirty: dirty}
			r.SourceRepo, r.SourceCommit = repo, full
		}
	} else {
		r.Warnings = append(r.Warnings, "release file has no SOURCE commit; only version evidence can be used")
	}

	if src.repo != "" && c.UpstreamGACommit != "" {
		ok, err := anc.IsAncestor(src.repo, c.UpstreamGACommit, src.commit)
		if err == nil {
			behind := !ok
			r.BehindUpstream = &behind
			if behind {
				r.Warnings = append(r.Warnings, fmt.Sprintf("build source does not contain upstream %s", c.UpstreamGA))
			}
		}
	}

	for _, a := range p.Advisories {
		al, ok := a.ForLine(c.Line.Major)
		if !ok {
			continue
		}
		f := evaluateAdvisory(p, a, al, c, src, anc, now)
		f.Blocking = p.Gate.Blocks(a.Severity) && (f.Status == FindingVulnerable || f.Status == FindingAwaiting)
		if f.Blocking && acceptedMitigation(p, a, c, now) {
			f.Blocking, f.Mitigated = false, true
			f.Evidence = append(f.Evidence, fmt.Sprintf("NOT FIXED: unfixed build accepted with mitigation %q by override", a.Mitigation.JDKJavaOptions))
		}
		r.Findings = append(r.Findings, f)
	}

	for _, o := range p.Overrides {
		if o.Line != c.Line.Major {
			continue
		}
		if !o.Active(now) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("override %s expired on %s and was ignored", o.File, o.Expires.Format("2006-01-02")))
			continue
		}
		if o.Kind == policy.OverrideBaseline {
			r.Findings = append(r.Findings, evaluateBaseline(o, c, src, anc))
		}
	}

	r.Status, r.Promotable, r.Reasons = summarize(r.Findings, c.Line.Supported(now))
	return r
}

type sourceInfo struct {
	repo, commit string
	dirty        bool
}

func evaluateAdvisory(p *policy.Policy, a policy.Advisory, al policy.AdvisoryLine, c Candidate, src sourceInfo, anc Ancestry, now time.Time) Finding {
	f := Finding{Advisory: a.Name(), Severity: a.Severity}
	if al.Status == "not-affected" {
		f.Status = FindingNotAffected
		f.Evidence = append([]string{al.Reason}, al.Evidence...)
		return f
	}

	for _, o := range p.Overrides {
		if o.Kind == policy.OverrideVerify && o.Advisory == a.ID && o.Line == c.Line.Major && o.Active(now) &&
			o.Distribution == c.Distribution && o.RuntimeVersion == c.Release["JAVA_RUNTIME_VERSION"] {
			f.Status = FindingFixed
			f.Evidence = append([]string{fmt.Sprintf("manual verification %s approved by %s: %s", o.File, o.ApprovedBy, o.Reason)}, o.Evidence...)
			return f
		}
	}

	if src.repo != "" && len(al.FixCommits) > 0 {
		missingInUpstream := false
		contained := 0
		for _, fc := range al.FixCommits {
			ok, err := anc.IsAncestor(src.repo, fc.Commit, src.commit)
			switch {
			case err == nil && ok:
				contained++
				f.Evidence = append(f.Evidence, fmt.Sprintf("source %s (%s) contains fix %s %s", short(src.commit), src.repo, short(fc.Commit), fc.Bug))
			case src.repo == fc.Repo && err == nil:
				missingInUpstream = true
				f.Evidence = append(f.Evidence, fmt.Sprintf("source %s (%s) does not contain fix %s %s", short(src.commit), src.repo, short(fc.Commit), fc.Bug))
			default:
				f.Evidence = append(f.Evidence, fmt.Sprintf("fix %s %s not found in history of %s (vendor may carry it under another commit)", short(fc.Commit), fc.Bug, src.repo))
			}
		}
		if contained == len(al.FixCommits) {
			f.Status = FindingFixed
			if src.dirty {
				f.Evidence = append(f.Evidence, "build has local patches on top of this source (release SOURCE ends in '+')")
			}
			return f
		}
		if missingInUpstream {
			f.Status = FindingVulnerable
			if src.dirty {
				f.Evidence = append(f.Evidence, "build has local patches; if the distribution backported the fix as a patch, record a reviewed `verify` override")
			}
			return f
		}
	}

	if c.DistVersion != "" {
		if fixed, ok := al.FixedVersions[c.Distribution]; ok && CompareVersions(c.DistVersion, fixed) >= 0 {
			f.Status = FindingFixed
			f.Evidence = append(f.Evidence, fmt.Sprintf("%s version %s >= declared fixed version %s", c.Distribution, c.DistVersion, fixed))
			return f
		}
		if through, ok := al.AffectedThrough[c.Distribution]; ok && CompareVersions(c.DistVersion, through) <= 0 {
			f.Status = FindingVulnerable
			f.Evidence = append(f.Evidence, fmt.Sprintf("%s version %s is within the declared affected range (through %s)", c.Distribution, c.DistVersion, through))
			return f
		}
		if fixed, ok := al.FixedVersions[c.Distribution]; ok {
			f.Status = FindingVulnerable
			f.Evidence = append(f.Evidence, fmt.Sprintf("%s version %s < declared fixed version %s", c.Distribution, c.DistVersion, fixed))
			return f
		}
	}

	f.Status = FindingAwaiting
	f.Evidence = append(f.Evidence, "no source-ancestry or vendor version evidence of a fix for this build")
	return f
}

func evaluateBaseline(o policy.Override, c Candidate, src sourceInfo, anc Ancestry) Finding {
	f := Finding{Advisory: "baseline " + firstNonEmpty(o.Advisory, o.File), Severity: "critical", Blocking: true}
	f.Evidence = append([]string{fmt.Sprintf("emergency baseline %s approved by %s: %s", o.File, o.ApprovedBy, o.Reason)}, o.Evidence...)
	if src.repo == "" {
		f.Status = FindingAwaiting
		return f
	}
	ok, err := anc.IsAncestor(src.repo, o.RequireCommit, src.commit)
	switch {
	case err == nil && ok:
		f.Status, f.Blocking = FindingFixed, false
	case err == nil && src.repo == c.Line.UpstreamRepo:
		f.Status = FindingVulnerable
	default:
		f.Status = FindingAwaiting
	}
	return f
}

func acceptedMitigation(p *policy.Policy, a policy.Advisory, c Candidate, now time.Time) bool {
	if a.Mitigation == nil || !strings.Contains(c.JDKJavaOptions, a.Mitigation.JDKJavaOptions) {
		return false
	}
	for _, o := range p.Overrides {
		if o.Kind == policy.OverrideAcceptMitigated && o.Advisory == a.ID && o.Line == c.Line.Major && o.Active(now) {
			return true
		}
	}
	return false
}

func summarize(findings []Finding, supported bool) (status string, promotable bool, reasons []string) {
	status = StatusVerified
	for _, f := range findings {
		switch f.Status {
		case FindingVulnerable:
			status = StatusVulnerable
		case FindingAwaiting:
			if status != StatusVulnerable {
				status = StatusAwaiting
			}
		}
		if f.Blocking {
			reasons = append(reasons, fmt.Sprintf("%s (%s) is %s", f.Advisory, f.Severity, f.Status))
		}
	}
	if !supported {
		status = StatusUnsupported
		reasons = append(reasons, "Java line is past its security support date")
	}
	sort.Strings(reasons)
	return status, len(reasons) == 0, reasons
}

var (
	numRE  = regexp.MustCompile(`\d+`)
	mainRE = regexp.MustCompile(`^\d+(?:\.\d+)*`)
)

// CompareVersions compares two versions of the same scheme. The leading
// dotted numeric part is compared first (missing components count as zero,
// so 17.0.21 == 17.0.21.0), then any remaining numbers such as a "+9" build
// or "-r1" package revision. This orders JEP 322 versions correctly:
// 17.0.21.1+1 > 17.0.21+9.
func CompareVersions(a, b string) int {
	ma, mb := mainRE.FindString(a), mainRE.FindString(b)
	if c := compareNumbers(numRE.FindAllString(ma, -1), numRE.FindAllString(mb, -1)); c != 0 {
		return c
	}
	return compareNumbers(numRE.FindAllString(a[len(ma):], -1), numRE.FindAllString(b[len(mb):], -1))
}

func compareNumbers(na, nb []string) int {
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y int
		if i < len(na) {
			x, _ = strconv.Atoi(na[i])
		}
		if i < len(nb) {
			y, _ = strconv.Atoi(nb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// LatestGA returns the newest tag matching pattern, ordered by the numeric
// components of its first capture group.
func LatestGA(tags map[string]string, pattern string) (name, commit string) {
	re := regexp.MustCompile(pattern)
	best := ""
	for tag, c := range tags {
		m := re.FindStringSubmatch(tag)
		if m == nil || len(m) < 2 {
			continue
		}
		if best == "" || CompareVersions(m[1], re.FindStringSubmatch(best)[1]) > 0 {
			best, commit = tag, c
		}
	}
	return best, commit
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

var _ Ancestry = (*gitcache.Cache)(nil)

// Package policy loads and validates the security policy in policy/: the Java
// lines that are built, the permitted JDK distributions, advisories, gate
// settings and reviewed overrides.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Line struct {
	Major                int       `yaml:"major"`
	Tag                  string    `yaml:"tag"`
	LTS                  bool      `yaml:"lts"`
	SecuritySupportUntil time.Time `yaml:"security_support_until"`
	SupportEvidence      []string  `yaml:"support_evidence"`
	UpstreamRepo         string    `yaml:"upstream_repo"`
	UpstreamTagPattern   string    `yaml:"upstream_tag_pattern"`
	JavaHome             string    `yaml:"java_home"`
	DisableExecutables   []string  `yaml:"disable_executables"`
	// ExtraPackages are added for Wolfi builds when the JDK package's own
	// dependencies miss something a compatibility test requires.
	ExtraPackages []string `yaml:"extra_packages"`
}

// Supported reports whether the line is inside its security support window.
func (l Line) Supported(now time.Time) bool {
	return now.Before(l.SecuritySupportUntil.Add(24 * time.Hour))
}

type UnsupportedLine struct {
	Major  int    `yaml:"major"`
	Reason string `yaml:"reason"`
}

type Distribution struct {
	Name               string            `yaml:"-"`
	Kind               string            `yaml:"kind"`
	Packages           []string          `yaml:"packages"`
	SourceRepos        []string          `yaml:"source_repos"`
	ReleasesAPI        string            `yaml:"releases_api"`
	API                string            `yaml:"api"`
	ArchiveURL         string            `yaml:"archive_url"`
	SignatureURL       string            `yaml:"signature_url"`
	ArchNames          map[string]string `yaml:"arch_names"`
	SigningKey         string            `yaml:"signing_key"`
	SigningFingerprint string            `yaml:"signing_fingerprint"`
	License            string            `yaml:"license"`
}

// Expand substitutes {major} and {upstream_repo} for a line.
func Expand(s string, l Line) string {
	return strings.NewReplacer("{major}", strconv.Itoa(l.Major), "{upstream_repo}", l.UpstreamRepo).Replace(s)
}

func (d Distribution) ReposFor(l Line) []string {
	out := make([]string, 0, len(d.SourceRepos))
	for _, r := range d.SourceRepos {
		out = append(out, Expand(r, l))
	}
	return out
}

type FixCommit struct {
	Repo   string `yaml:"repo"`
	Commit string `yaml:"commit"`
	Bug    string `yaml:"bug"`
}

type AdvisoryLine struct {
	Status        string            `yaml:"status"` // affected | not-affected
	Reason        string            `yaml:"reason"`
	Evidence      []string          `yaml:"evidence"`
	Introduced    string            `yaml:"introduced"`
	FixCommits    []FixCommit       `yaml:"fix_commits"`
	FixedVersions map[string]string `yaml:"fixed_versions"`
	// AffectedThrough lists, per distribution, the newest version a vendor or
	// advisory has declared affected (inclusive).
	AffectedThrough map[string]string `yaml:"affected_through"`
}

type Mitigation struct {
	JDKJavaOptions string   `yaml:"jdk_java_options"`
	Evidence       []string `yaml:"evidence"`
}

type Advisory struct {
	ID         string                  `yaml:"id"`
	Aliases    []string                `yaml:"aliases"`
	Severity   string                  `yaml:"severity"`
	Published  time.Time               `yaml:"published"`
	Summary    string                  `yaml:"summary"`
	References []string                `yaml:"references"`
	Lines      map[string]AdvisoryLine `yaml:"lines"`
	Mitigation *Mitigation             `yaml:"mitigation"`
	File       string                  `yaml:"-"`
}

func (a Advisory) ForLine(major int) (AdvisoryLine, bool) {
	al, ok := a.Lines[strconv.Itoa(major)]
	return al, ok
}

func (a Advisory) Name() string {
	if len(a.Aliases) > 0 {
		return a.ID + " (" + strings.Join(a.Aliases, ", ") + ")"
	}
	return a.ID
}

const (
	OverrideBaseline        = "baseline"
	OverrideVerify          = "verify"
	OverrideAcceptMitigated = "accept-mitigated"
)

type Override struct {
	Kind           string    `yaml:"kind"`
	Advisory       string    `yaml:"advisory"`
	Line           int       `yaml:"line"`
	RequireCommit  string    `yaml:"require_commit"`
	Distribution   string    `yaml:"distribution"`
	RuntimeVersion string    `yaml:"runtime_version"`
	Reason         string    `yaml:"reason"`
	Evidence       []string  `yaml:"evidence"`
	ApprovedBy     string    `yaml:"approved_by"`
	Expires        time.Time `yaml:"expires"`
	File           string    `yaml:"-"`
}

func (o Override) Active(now time.Time) bool {
	return now.Before(o.Expires.Add(24 * time.Hour))
}

type Gate struct {
	BlockingSeverities       []string `yaml:"blocking_severities"`
	UpstreamLagFallbackHours int      `yaml:"upstream_lag_fallback_hours"`
	PublicationTargetHours   int      `yaml:"publication_target_hours"`
}

func (g Gate) Blocks(severity string) bool {
	for _, s := range g.BlockingSeverities {
		if s == severity {
			return true
		}
	}
	return false
}

type Policy struct {
	Lines         []Line
	Unsupported   []UnsupportedLine
	Default       string
	FallbackOrder []string
	Distributions map[string]Distribution
	Advisories    []Advisory
	Overrides     []Override
	Gate          Gate
	// Digest covers every policy file, so any policy change is a build input.
	Digest string
}

func (p *Policy) Line(major int) (Line, bool) {
	for _, l := range p.Lines {
		if l.Major == major {
			return l, true
		}
	}
	return Line{}, false
}

// Candidates returns the distributions to try for a line, in order.
func (p *Policy) Candidates() []string {
	return append([]string{p.Default}, p.FallbackOrder...)
}

var (
	commitRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	severities = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
)

// Load reads and validates a policy directory.
func Load(dir string) (*Policy, error) {
	h := sha256.New()
	p := &Policy{}

	var lines struct {
		Lines       []Line            `yaml:"lines"`
		Unsupported []UnsupportedLine `yaml:"unsupported"`
	}
	if err := decodeFile(filepath.Join(dir, "java-lines.yaml"), &lines, h); err != nil {
		return nil, err
	}
	p.Lines, p.Unsupported = lines.Lines, lines.Unsupported

	var dists struct {
		Default       string                  `yaml:"default"`
		FallbackOrder []string                `yaml:"fallback_order"`
		Distributions map[string]Distribution `yaml:"distributions"`
	}
	if err := decodeFile(filepath.Join(dir, "distributions.yaml"), &dists, h); err != nil {
		return nil, err
	}
	p.Default, p.FallbackOrder, p.Distributions = dists.Default, dists.FallbackOrder, dists.Distributions
	for name, d := range p.Distributions {
		d.Name = name
		p.Distributions[name] = d
	}

	if err := decodeFile(filepath.Join(dir, "gate.yaml"), &p.Gate, h); err != nil {
		return nil, err
	}

	advFiles, _ := filepath.Glob(filepath.Join(dir, "advisories", "*.yaml"))
	sort.Strings(advFiles)
	for _, f := range advFiles {
		var a Advisory
		if err := decodeFile(f, &a, h); err != nil {
			return nil, err
		}
		a.File = f
		p.Advisories = append(p.Advisories, a)
	}

	ovFiles, _ := filepath.Glob(filepath.Join(dir, "overrides", "*.yaml"))
	sort.Strings(ovFiles)
	for _, f := range ovFiles {
		var o Override
		if err := decodeFile(f, &o, h); err != nil {
			return nil, err
		}
		o.File = f
		p.Overrides = append(p.Overrides, o)
	}

	p.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func decodeFile(path string, v any, h io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(h, "%s\x00%d\x00", filepath.Base(path), len(data))
	h.Write(data)
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (p *Policy) validate() error {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	seenMajor, seenTag := map[int]bool{}, map[string]bool{}
	for _, l := range p.Lines {
		if l.Major <= 0 || seenMajor[l.Major] {
			bad("java-lines: invalid or duplicate major %d", l.Major)
		}
		if l.Tag == "" || seenTag[l.Tag] {
			bad("java-lines: invalid or duplicate tag %q", l.Tag)
		}
		seenMajor[l.Major], seenTag[l.Tag] = true, true
		if l.SecuritySupportUntil.IsZero() || len(l.SupportEvidence) == 0 {
			bad("java-lines %d: security_support_until and support_evidence are required", l.Major)
		}
		if !strings.HasPrefix(l.UpstreamRepo, "https://") {
			bad("java-lines %d: upstream_repo must be an https URL", l.Major)
		}
		if _, err := regexp.Compile(l.UpstreamTagPattern); err != nil || l.UpstreamTagPattern == "" {
			bad("java-lines %d: upstream_tag_pattern: %v", l.Major, err)
		}
		if !strings.HasPrefix(l.JavaHome, "/usr/lib/jvm/") {
			bad("java-lines %d: java_home must be under /usr/lib/jvm", l.Major)
		}
	}
	for _, u := range p.Unsupported {
		if seenMajor[u.Major] {
			bad("java-lines: %d is both built and unsupported", u.Major)
		}
	}

	for _, name := range p.Candidates() {
		if _, ok := p.Distributions[name]; !ok {
			bad("distributions: %q is referenced but not defined", name)
		}
	}
	for name, d := range p.Distributions {
		switch d.Kind {
		case "wolfi":
			if len(d.Packages) == 0 {
				bad("distribution %s: packages required", name)
			}
		case "corretto", "temurin":
			if d.SigningKey == "" || len(d.SigningFingerprint) != 40 {
				bad("distribution %s: signing_key and a full 40-hex signing_fingerprint are required", name)
			}
		default:
			bad("distribution %s: unknown kind %q", name, d.Kind)
		}
		if len(d.SourceRepos) == 0 {
			bad("distribution %s: source_repos required", name)
		}
	}

	if len(p.Gate.BlockingSeverities) == 0 {
		bad("gate: blocking_severities must not be empty")
	}

	for _, a := range p.Advisories {
		if a.ID == "" || !severities[a.Severity] {
			bad("%s: id and a valid severity are required", a.File)
		}
		for line, al := range a.Lines {
			major, err := strconv.Atoi(line)
			if err != nil {
				bad("%s: line key %q is not a number", a.File, line)
			}
			switch al.Status {
			case "not-affected":
				if al.Reason == "" || len(al.Evidence) == 0 {
					bad("%s line %s: not-affected requires reason and evidence", a.File, line)
				}
			case "affected":
				for _, fc := range al.FixCommits {
					if !commitRE.MatchString(fc.Commit) || !strings.HasPrefix(fc.Repo, "https://") {
						bad("%s line %s: fix commits need a full 40-hex commit and https repo", a.File, line)
					}
					if l, ok := p.Line(major); ok && fc.Repo != l.UpstreamRepo {
						bad("%s line %s: fix commit repo %s differs from the line's upstream_repo %s", a.File, line, fc.Repo, l.UpstreamRepo)
					}
				}
			default:
				bad("%s line %s: status must be affected or not-affected", a.File, line)
			}
		}
	}

	for _, o := range p.Overrides {
		if len(o.Evidence) == 0 || o.ApprovedBy == "" || o.Expires.IsZero() || o.Reason == "" {
			bad("%s: overrides require reason, evidence, approved_by and expires", o.File)
		}
		if _, ok := p.Line(o.Line); !ok {
			bad("%s: unknown line %d", o.File, o.Line)
		}
		switch o.Kind {
		case OverrideBaseline:
			if !commitRE.MatchString(o.RequireCommit) {
				bad("%s: baseline requires a full 40-hex require_commit", o.File)
			}
		case OverrideVerify:
			if o.Advisory == "" || o.Distribution == "" || o.RuntimeVersion == "" {
				bad("%s: verify requires advisory, distribution and runtime_version", o.File)
			}
		case OverrideAcceptMitigated:
			if o.Advisory == "" {
				bad("%s: accept-mitigated requires advisory", o.File)
			}
			if a := p.advisory(o.Advisory); a == nil || a.Mitigation == nil {
				bad("%s: advisory %s has no mitigation to accept", o.File, o.Advisory)
			}
		default:
			bad("%s: unknown kind %q", o.File, o.Kind)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid policy:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func (p *Policy) advisory(id string) *Advisory {
	for i := range p.Advisories {
		if p.Advisories[i].ID == id {
			return &p.Advisories[i]
		}
	}
	return nil
}

// ActiveMitigations returns JDK_JAVA_OPTIONS fragments that maintainers have
// explicitly accepted for a line through accept-mitigated overrides.
func (p *Policy) ActiveMitigations(major int, now time.Time) []string {
	var out []string
	for _, o := range p.Overrides {
		if o.Kind != OverrideAcceptMitigated || o.Line != major || !o.Active(now) {
			continue
		}
		if a := p.advisory(o.Advisory); a != nil && a.Mitigation != nil {
			out = append(out, a.Mitigation.JDKJavaOptions)
		}
	}
	return out
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ctyl/internal/evaluate"
	"ctyl/internal/gitcache"
	"ctyl/internal/launch"
	"ctyl/internal/ociimage"
	"ctyl/internal/policy"
	"ctyl/internal/render"
)

// GateResult is the multi-architecture gate outcome for one image tarball.
type GateResult struct {
	Line         int                           `json:"line"`
	Tag          string                        `json:"tag"`
	Distribution string                        `json:"distribution"`
	IndexDigest  string                        `json:"index_digest"`
	Status       string                        `json:"status"`
	Promotable   bool                          `json:"promotable"`
	Reasons      []string                      `json:"reasons,omitempty"`
	PolicyDigest string                        `json:"policy_digest"`
	UpstreamGA   string                        `json:"upstream_latest_ga,omitempty"`
	BehindUp     bool                          `json:"behind_upstream"`
	Reports      []evaluate.Report             `json:"architectures"`
	Packages     map[string][]ociimage.Package `json:"packages"`
	EvaluatedAt  time.Time                     `json:"evaluated_at"`
}

var ociToApk = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

func gateImage(c commonFlags, image string, major int, dist string) (*GateResult, error) {
	p, l, err := loadLine(c, major)
	if err != nil {
		return nil, err
	}
	d, ok := p.Distributions[dist]
	if !ok {
		return nil, fmt.Errorf("unknown distribution %q", dist)
	}
	now, err := c.time()
	if err != nil {
		return nil, err
	}
	images, err := ociimage.Open(image)
	if err != nil {
		return nil, err
	}
	digest, err := ociimage.IndexDigest(image)
	if err != nil {
		return nil, err
	}
	cache := gitcache.New(c.gitCache)
	tags, err := cache.Tags(l.UpstreamRepo)
	if err != nil {
		return nil, fmt.Errorf("upstream release monitoring for Java %d: %w", major, err)
	}
	ga, gaCommit := evaluate.LatestGA(tags, l.UpstreamTagPattern)

	res := &GateResult{Line: major, Tag: l.Tag, Distribution: dist, IndexDigest: digest, PolicyDigest: p.Digest,
		UpstreamGA: ga, Packages: map[string][]ociimage.Package{}, EvaluatedAt: now, Promotable: true}
	distPackage := render.VendorPackageName(dist, major)
	if d.Kind == "wolfi" {
		distPackage = policy.Expand(d.Packages[0], l)
	}

	var sources, runtimes []string
	for _, img := range images {
		arch := ociToApk[img.Arch]
		javaHome, _ := img.Config.Env("JAVA_HOME")
		data, err := img.ReadFile(filepath.Join(javaHome, "release"))
		if err != nil {
			return nil, fmt.Errorf("%s: reading JDK release file: %w", arch, err)
		}
		rel, err := launch.ParseRelease(strings.NewReader(string(data)))
		if err != nil {
			return nil, err
		}
		pkgs, err := img.Packages()
		if err != nil {
			return nil, err
		}
		res.Packages[arch] = pkgs
		distVersion := ""
		for _, pkg := range pkgs {
			if pkg.Name == distPackage {
				distVersion = pkg.Version
			}
		}
		if d.Kind != "wolfi" {
			distVersion = apkRevisionRE.ReplaceAllString(distVersion, "")
		}
		opts, _ := img.Config.Env("JDK_JAVA_OPTIONS")
		r := evaluate.Evaluate(p, evaluate.Candidate{
			Line: l, Distribution: dist, Arch: arch, Release: rel, DistVersion: distVersion,
			JDKJavaOptions: opts, UpstreamGA: ga, UpstreamGACommit: gaCommit,
		}, cache, now)
		if distVersion == "" {
			r.Warnings = append(r.Warnings, fmt.Sprintf("package %s not installed; distribution version unknown", distPackage))
		}
		if r.BehindUpstream != nil && *r.BehindUpstream {
			res.BehindUp = true
		}
		res.Reports = append(res.Reports, r)
		sources = append(sources, r.SourceCommit)
		runtimes = append(runtimes, r.RuntimeVersion)
		if !r.Promotable {
			res.Promotable = false
			for _, reason := range r.Reasons {
				res.Reasons = append(res.Reasons, arch+": "+reason)
			}
		}
	}
	if !allEqual(sources) || !allEqual(runtimes) {
		res.Promotable = false
		res.Reasons = append(res.Reasons, fmt.Sprintf("architectures carry different JDK builds: %v %v", runtimes, sources))
	}
	res.Status = worstStatus(res.Reports)
	return res, nil
}

func allEqual(s []string) bool {
	for _, x := range s {
		if x != s[0] {
			return false
		}
	}
	return true
}

var statusRank = map[string]int{
	evaluate.StatusVerified: 0, evaluate.StatusAwaiting: 1, evaluate.StatusVulnerable: 2, evaluate.StatusUnsupported: 3,
}

func worstStatus(reports []evaluate.Report) string {
	worst := evaluate.StatusVerified
	for _, r := range reports {
		if statusRank[r.Status] > statusRank[worst] {
			worst = r.Status
		}
	}
	return worst
}

func cmdGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	image := fs.String("image", "", "apko image tarball")
	major := fs.Int("line", 0, "Java major version")
	dist := fs.String("distribution", "", "distribution the image was built from")
	out := fs.String("out", "", "write the JSON report here")
	summary := fs.String("summary", "", "append a Markdown summary here (e.g. $GITHUB_STEP_SUMMARY)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := gateImage(c, *image, *major, *dist)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := writeJSON(*out, res); err != nil {
			return err
		}
	}
	md := gateMarkdown(res)
	fmt.Fprint(os.Stderr, md)
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprint(f, md)
		f.Close()
	}
	if !res.Promotable {
		return &gateRejected{fmt.Sprintf("Java %d (%s) is not promotable: %s", res.Line, res.Distribution, strings.Join(res.Reasons, "; "))}
	}
	return nil
}

func gateMarkdown(r *GateResult) string {
	var b strings.Builder
	verdict := "PASS"
	if !r.Promotable {
		verdict = "BLOCKED"
	}
	fmt.Fprintf(&b, "### %s (%s): %s — %s\n\n", r.Tag, r.Distribution, r.Status, verdict)
	fmt.Fprintf(&b, "Index digest `%s`; upstream latest GA `%s`; behind upstream: %v\n\n", r.IndexDigest, r.UpstreamGA, r.BehindUp)
	fmt.Fprintln(&b, "| arch | runtime | source | advisory | finding | evidence |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|")
	for _, a := range r.Reports {
		src := a.SourceCommit
		if len(src) > 12 {
			src = src[:12]
		}
		if len(a.Findings) == 0 {
			fmt.Fprintf(&b, "| %s | %s | %s | — | no known advisories | |\n", a.Arch, a.RuntimeVersion, src)
		}
		for _, f := range a.Findings {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s%s | %s |\n", a.Arch, a.RuntimeVersion, src, f.Advisory, f.Status,
				map[bool]string{true: " (blocking)", false: ""}[f.Blocking], strings.ReplaceAll(strings.Join(f.Evidence, "; "), "|", "\\|"))
		}
		for _, w := range a.Warnings {
			fmt.Fprintf(&b, "\n> %s: %s", a.Arch, w)
		}
	}
	for _, reason := range r.Reasons {
		fmt.Fprintf(&b, "\n- **blocked:** %s", reason)
	}
	b.WriteString("\n\n")
	return b.String()
}

// State is the published record of a tag, kept on the release-state branch.
type State struct {
	Line                   int                `json:"line"`
	Tag                    string             `json:"tag"`
	Digest                 string             `json:"digest"`
	ImmutableTags          []string           `json:"immutable_tags"`
	Distribution           string             `json:"distribution"`
	DistributionVersion    string             `json:"distribution_version"`
	RuntimeVersion         string             `json:"runtime_version"`
	JavaReleaseDate        string             `json:"java_release_date,omitempty"`
	RebuildReasons         []string           `json:"rebuild_reasons,omitempty"`
	SourceCommit           string             `json:"source_commit"`
	SourceRepo             string             `json:"source_repo"`
	InputsDigest           string             `json:"inputs_digest"`
	DefaultCandidateInputs string             `json:"default_candidate_inputs,omitempty"`
	PolicyDigest           string             `json:"policy_digest"`
	Status                 string             `json:"status"`
	Promoted               bool               `json:"promoted"`
	Reasons                []string           `json:"reasons,omitempty"`
	Findings               []evaluate.Finding `json:"findings"`
	UpstreamGA             string             `json:"upstream_latest_ga"`
	UpstreamGADate         string             `json:"upstream_latest_ga_date,omitempty"`
	BehindUpstream         bool               `json:"behind_upstream"`
	Revision               string             `json:"revision"`
	RunURL                 string             `json:"run_url,omitempty"`
	BuiltAt                string             `json:"built_at"`
	PublishedAt            string             `json:"published_at,omitempty"`
	// LatencyHours is publication time minus the upstream GA tag time.
	LatencyHours *float64            `json:"latency_hours,omitempty"`
	CheckedAt    string              `json:"checked_at,omitempty"`
	Packages     map[string][]string `json:"packages"`
}

func latencyHours(gaDate, publishedAt string) *float64 {
	ga, err1 := time.Parse(time.RFC3339, gaDate)
	pub, err2 := time.Parse(time.RFC3339, publishedAt)
	if err1 != nil || err2 != nil {
		return nil
	}
	h := float64(pub.Sub(ga).Round(6*time.Minute)) / float64(time.Hour)
	return &h
}

func cmdMetadata(args []string) error {
	fs := flag.NewFlagSet("metadata", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	report := fs.String("report", "", "gate report JSON")
	inputs := fs.String("inputs-digest", "", "inputs digest of the build")
	defaultInputs := fs.String("default-candidate-inputs", "", "inputs digest of the rejected default-distribution candidate, if any")
	revision := fs.String("revision", "", "source revision (git commit)")
	runURL := fs.String("run-url", "", "CI run URL")
	digest := fs.String("digest", "", "published index digest (must equal the gated digest)")
	tags := fs.String("tags", "", "comma-separated immutable tags")
	promoted := fs.Bool("promoted", false, "whether the floating tag now points at this digest")
	previous := fs.String("previous", "", "the tag's previous state JSON; publication details are kept when the digest is unchanged")
	out := fs.String("out", "", "output state JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var g GateResult
	data, err := os.ReadFile(*report)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return err
	}
	if *digest != "" && *digest != g.IndexDigest {
		return fmt.Errorf("published digest %s differs from gated digest %s: refusing to record an untested image", *digest, g.IndexDigest)
	}
	now, err := c.time()
	if err != nil {
		return err
	}
	s := State{Line: g.Line, Tag: g.Tag, Digest: g.IndexDigest, ImmutableTags: splitList(*tags), Distribution: g.Distribution,
		InputsDigest: *inputs, DefaultCandidateInputs: *defaultInputs, PolicyDigest: g.PolicyDigest, Status: g.Status,
		Promoted: *promoted, Reasons: g.Reasons, UpstreamGA: g.UpstreamGA, BehindUpstream: g.BehindUp,
		Revision: *revision, RunURL: *runURL, BuiltAt: g.EvaluatedAt.Format(time.RFC3339), Packages: map[string][]string{}}
	if *promoted || *digest != "" {
		s.PublishedAt = now.Format(time.RFC3339)
	}
	if len(g.Reports) > 0 {
		r := g.Reports[0]
		s.DistributionVersion, s.RuntimeVersion, s.SourceCommit, s.SourceRepo, s.Findings = r.DistVersion, r.RuntimeVersion, r.SourceCommit, r.SourceRepo, r.Findings
		s.JavaReleaseDate = r.JavaReleaseDate
	}
	for arch, pkgs := range g.Packages {
		for _, p := range pkgs {
			s.Packages[arch] = append(s.Packages[arch], p.Name+"="+p.Version)
		}
	}
	if p, l, err := loadLine(c, g.Line); err == nil && g.UpstreamGA != "" {
		_ = p
		if date, err := gitcache.New(c.gitCache).TagDate(l.UpstreamRepo, g.UpstreamGA); err == nil {
			s.UpstreamGADate = date
		}
	}
	s.CheckedAt = now.Format(time.RFC3339)
	if s.PublishedAt != "" {
		s.LatencyHours = latencyHours(s.UpstreamGADate, s.PublishedAt)
	}
	// Re-confirming an already published digest (daily rebuilds reproduce it)
	// is not a new publication: keep when and by which run it was published.
	s.RebuildReasons = rebuildReasons(State{}, s)
	if *previous != "" {
		if data, err := os.ReadFile(*previous); err == nil {
			var prev State
			if json.Unmarshal(data, &prev) == nil {
				s.RebuildReasons = rebuildReasons(prev, s)
			}
			if prev.Digest == s.Digest && prev.PublishedAt != "" {
				s.RebuildReasons = prev.RebuildReasons
				s.BuiltAt, s.PublishedAt, s.LatencyHours, s.RunURL, s.Revision = prev.BuiltAt, prev.PublishedAt, prev.LatencyHours, prev.RunURL, prev.Revision
			}
		}
	}
	return writeJSON(*out, s)
}

// cmdMark records a gate rejection against the line's published state: the
// floating tag keeps pointing at the last published digest, but its recorded
// status now reflects the newest evaluation (e.g. vulnerable, no fix yet).
func cmdMark(args []string) error {
	fs := flag.NewFlagSet("mark", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	report := fs.String("report", "", "gate report of the rejected candidate")
	state := fs.String("state", "", "existing state JSON (updated in place; created if missing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var g GateResult
	data, err := os.ReadFile(*report)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return err
	}
	var s State
	if data, err := os.ReadFile(*state); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
	} else {
		s = State{Line: g.Line, Tag: g.Tag, Packages: map[string][]string{}}
	}
	now, err := c.time()
	if err != nil {
		return err
	}
	s.Status, s.Reasons, s.PolicyDigest, s.CheckedAt = g.Status, g.Reasons, g.PolicyDigest, now.Format(time.RFC3339)
	s.UpstreamGA, s.BehindUpstream = g.UpstreamGA, g.BehindUp
	if len(g.Reports) > 0 {
		s.Findings = g.Reports[0].Findings
	}
	if s.Status == evaluate.StatusVerified && !g.Promotable {
		s.Status = evaluate.StatusAwaiting
	}
	return writeJSON(*state, s)
}

func cmdUpstream(args []string) error {
	fs := flag.NewFlagSet("upstream", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	cache := gitcache.New(c.gitCache)
	type entry struct {
		Major  int    `json:"major"`
		Tag    string `json:"ga_tag"`
		Commit string `json:"commit"`
		Date   string `json:"date"`
		Error  string `json:"error,omitempty"`
	}
	var out []entry
	for _, l := range p.Lines {
		e := entry{Major: l.Major}
		tags, err := cache.Tags(l.UpstreamRepo)
		if err != nil {
			e.Error = err.Error()
		} else {
			e.Tag, e.Commit = evaluate.LatestGA(tags, l.UpstreamTagPattern)
			e.Date, _ = cache.TagDate(l.UpstreamRepo, e.Tag)
		}
		out = append(out, e)
	}
	return writeJSON("", out)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	stateDir := fs.String("state", "state", "directory of published state JSON files")
	out := fs.String("out", "", "Markdown output (default stdout)")
	readme := fs.String("readme", "", "rewrite the generated status section of this file in place")
	image := fs.String("image", "", "image repository for copy-paste references, e.g. ghcr.io/owner/ctyl")
	if err := fs.Parse(args); err != nil {
		return err
	}
	states, err := loadStates(*stateDir)
	if err != nil {
		return err
	}
	if *readme == "" {
		return writeOut(*out, []byte(statusTable(states)))
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*readme)
	if err != nil {
		return err
	}
	updated, err := replaceSection(string(data), readmeStatus(p, states, *image))
	if err != nil {
		return fmt.Errorf("%s: %w", *readme, err)
	}
	return os.WriteFile(*readme, []byte(updated), 0o644)
}

func loadStates(dir string) ([]State, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "java_*.json"))
	var states []State
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Line < states[j].Line })
	return states, nil
}

func statusTable(states []State) string {
	var b strings.Builder
	fmt.Fprintln(&b, "| tag | status | java | distribution | digest | upstream GA | published |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|")
	for _, s := range states {
		status := s.Status
		if !s.Promoted {
			status += " (not promoted)"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s %s | `%s` | %s | %s |\n", s.Tag, status, s.RuntimeVersion, s.Distribution, s.DistributionVersion, short(s.Digest), s.UpstreamGA, s.PublishedAt)
	}
	return b.String()
}

const (
	statusBegin = "<!-- ctyl:status:begin -->"
	statusEnd   = "<!-- ctyl:status:end -->"
)

func replaceSection(doc, section string) (string, error) {
	i, j := strings.Index(doc, statusBegin), strings.Index(doc, statusEnd)
	if i < 0 || j < i {
		return "", fmt.Errorf("markers %s / %s not found", statusBegin, statusEnd)
	}
	return doc[:i+len(statusBegin)] + "\n" + section + doc[j:], nil
}

var statusBadge = map[string]string{
	evaluate.StatusVerified:    "✅ verified",
	evaluate.StatusAwaiting:    "⏳ awaiting verification",
	evaluate.StatusVulnerable:  "❌ vulnerable",
	evaluate.StatusUnsupported: "⛔ unsupported",
}

func displayTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// readmeStatus renders the README section from the release state only, so
// it changes exactly when the recorded state changes.
func readmeStatus(p *policy.Policy, states []State, image string) string {
	byTag := map[string]State{}
	latest := ""
	for _, s := range states {
		byTag[s.Tag] = s
		if s.PublishedAt > latest {
			latest = s.PublishedAt
		}
	}
	var b strings.Builder
	stateLink := "`release-state` branch"
	if repo, ok := strings.CutPrefix(image, "ghcr.io/"); ok {
		stateLink = fmt.Sprintf("[`release-state`](https://github.com/%s/tree/release-state) branch", repo)
	}
	fmt.Fprintf(&b, "_Generated from the %s by the release pipeline. Last publication: %s._\n\n", stateLink, displayTime(latest))
	fmt.Fprintln(&b, "| Image | Java (release date) | Status | Change since previous image | Container built | Image published | Digest |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|")
	var pinned []string
	for _, l := range p.Lines {
		ref := fmt.Sprintf("%s:%s", image, l.Tag)
		s, ok := byTag[l.Tag]
		if !ok || s.Digest == "" {
			fmt.Fprintf(&b, "| `%s` | %d | not published yet | — | — | — | — |\n", ref, l.Major)
			continue
		}
		status := statusBadge[s.Status]
		if status == "" {
			status = s.Status
		}
		if !s.Promoted {
			status += " (tag not moved)"
		}
		published := displayTime(s.PublishedAt)
		if s.RunURL != "" {
			published = fmt.Sprintf("[%s](%s)", published, s.RunURL)
		}
		java := strings.SplitN(s.RuntimeVersion, "+", 2)[0]
		releaseDate := "unknown"
		if date, err := time.Parse("2006-01-02", s.JavaReleaseDate); err == nil {
			releaseDate = date.Format("2006-01-02")
		}
		change := strings.Join(s.RebuildReasons, "; ")
		if change == "" {
			change = "not recorded"
		}
		if s.BehindUpstream {
			java += "; behind upstream " + s.UpstreamGA
		}
		fmt.Fprintf(&b, "| `%s` | %s (%s %s)<br>%s | %s | %s | %s | %s | `%s` |\n", ref, java, s.Distribution, s.DistributionVersion, releaseDate,
			status, change, displayTime(s.BuiltAt), published, short(s.Digest))
		pinned = append(pinned, fmt.Sprintf("%s@%s", ref, s.Digest))
	}
	fmt.Fprintln(&b, "\nJava release date is JAVA_VERSION_DATE reported by the bundled runtime, not the Wolfi package or container publication date. Unknown dates and unrecorded changes are not inferred from build times. Changes compare published image inputs; a Wolfi package rebuild can keep the same Java source. Identical daily rebuilds keep the original container build and publication times.")
	if len(pinned) > 0 {
		fmt.Fprintf(&b, "\n<details><summary>Pinned references (exact digests)</summary>\n\n```\n%s\n```\n\n</details>\n", strings.Join(pinned, "\n"))
	}
	return b.String()
}

func short(d string) string {
	if i := strings.Index(d, ":"); i >= 0 && len(d) > i+13 {
		return d[:i+13]
	}
	return d
}

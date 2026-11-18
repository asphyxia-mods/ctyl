package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ctyl/internal/evaluate"
	"ctyl/internal/gitcache"
	"ctyl/internal/policy"
	"ctyl/internal/registry"
)

var apkRevisionRE = regexp.MustCompile(`-r\d+$`)

// PlanEntry asks CI to run the build pipeline for one line.
type PlanEntry struct {
	Major       int      `json:"major"`
	Tag         string   `json:"tag"`
	Reasons     []string `json:"reasons"`
	PreferFresh bool     `json:"prefer_fresh"`
}

type Plan struct {
	Build  []PlanEntry `json:"build"`
	Alerts []string    `json:"alerts"`
	// Unchanged lists lines whose published image is current.
	Unchanged []string `json:"unchanged"`
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	stateDir := fs.String("state", "state", "release-state directory with java_<N>.json records")
	locks := fs.String("locks", "", "directory with fresh java_<N>.apko.yaml and java_<N>.lock.json (rendered + apko lock)")
	repo := fs.String("registry", "", "image repository to cross-check published digests (optional)")
	only := fs.String("lines", "", "restrict to these majors (comma-separated)")
	force := fs.Bool("force", false, "rebuild every selected line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	now, err := c.time()
	if err != nil {
		return err
	}
	selected := map[int]bool{}
	for _, s := range splitList(*only) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("invalid line %q", s)
		}
		selected[n] = true
	}

	cache := gitcache.New(c.gitCache)
	plan := Plan{Build: []PlanEntry{}, Alerts: []string{}, Unchanged: []string{}}
	for _, l := range p.Lines {
		if len(selected) > 0 && !selected[l.Major] {
			continue
		}
		if !l.Supported(now) {
			plan.Alerts = append(plan.Alerts, fmt.Sprintf("%s: Java %d passed its security support date %s; the tag is no longer rebuilt and is marked unsupported",
				l.Tag, l.Major, l.SecuritySupportUntil.Format("2006-01-02")))
			continue
		}
		if until := l.SecuritySupportUntil.Sub(now); until < 60*24*time.Hour {
			plan.Alerts = append(plan.Alerts, fmt.Sprintf("%s: security support ends %s; review support_evidence or announce retirement", l.Tag, l.SecuritySupportUntil.Format("2006-01-02")))
		}
		entry, alerts := planLine(c, p, l, cache, *stateDir, *locks, *repo, *force, now)
		plan.Alerts = append(plan.Alerts, alerts...)
		if len(entry.Reasons) > 0 {
			plan.Build = append(plan.Build, entry)
		} else {
			plan.Unchanged = append(plan.Unchanged, l.Tag)
		}
	}
	return writeJSON("", plan)
}

func planLine(c commonFlags, p *policy.Policy, l policy.Line, cache *gitcache.Cache, stateDir, locks, repo string, force bool, now time.Time) (PlanEntry, []string) {
	e := PlanEntry{Major: l.Major, Tag: l.Tag}
	var alerts []string
	reason := func(format string, args ...any) { e.Reasons = append(e.Reasons, fmt.Sprintf(format, args...)) }

	var st State
	data, err := os.ReadFile(filepath.Join(stateDir, l.Tag+".json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason("no published image recorded")
	case err != nil:
		alerts = append(alerts, fmt.Sprintf("%s: reading state: %v", l.Tag, err))
		reason("state unreadable")
	default:
		if err := json.Unmarshal(data, &st); err != nil {
			reason("state unparsable: %v", err)
		}
	}
	if force {
		reason("forced")
	}

	if locks != "" {
		fresh, err := inputsDigest(c.root, filepath.Join(locks, l.Tag+".apko.yaml"), filepath.Join(locks, l.Tag+".lock.json"), "")
		switch {
		case err != nil:
			alerts = append(alerts, fmt.Sprintf("%s: fresh resolution failed: %v", l.Tag, err))
			reason("fresh resolution failed; rebuilding to surface the error")
		case fresh != st.InputsDigest && fresh != st.DefaultCandidateInputs:
			reason("Wolfi resolution or image sources changed (%s)", short(fresh))
		}
	}
	if st.PolicyDigest != "" && st.PolicyDigest != p.Digest {
		reason("security policy changed")
	}
	if st.Status != "" && st.Status != evaluate.StatusVerified {
		reason("published image is %s; retrying for a fixed build", st.Status)
	}

	tags, err := cache.Tags(l.UpstreamRepo)
	if err != nil {
		alerts = append(alerts, fmt.Sprintf("%s: upstream monitoring failed: %v", l.Tag, err))
	} else if ga, gaCommit := evaluate.LatestGA(tags, l.UpstreamTagPattern); ga != "" && st.SourceRepo != "" && gitcache.ValidRev(st.SourceCommit) {
		contained, err := cache.IsAncestor(st.SourceRepo, gaCommit, st.SourceCommit)
		if err == nil && !contained {
			date, _ := cache.TagDate(l.UpstreamRepo, ga)
			age := time.Duration(0)
			if t, err := time.Parse(time.RFC3339, date); err == nil {
				age = now.Sub(t)
			}
			reason("upstream %s (tagged %s) is not in the published build", ga, date)
			if age > time.Duration(p.Gate.UpstreamLagFallbackHours)*time.Hour {
				e.PreferFresh = true
				target := time.Duration(p.Gate.PublicationTargetHours) * time.Hour
				if age > target {
					alerts = append(alerts, fmt.Sprintf("%s: upstream %s released %s ago is still not published (target %s)", l.Tag, ga, age.Round(time.Minute), target))
				}
			}
		}
	}

	if repo != "" && st.Digest != "" && st.Promoted {
		m, err := registry.Get(repo, l.Tag)
		switch {
		case errors.Is(err, registry.ErrNotFound):
			alerts = append(alerts, fmt.Sprintf("%s: recorded as published but missing from %s", l.Tag, repo))
			reason("tag missing from registry")
		case err != nil:
			alerts = append(alerts, fmt.Sprintf("%s: registry check failed: %v", l.Tag, err))
		case m.Digest != st.Digest:
			alerts = append(alerts, fmt.Sprintf("%s: registry digest %s differs from recorded %s", l.Tag, m.Digest, st.Digest))
			reason("registry digest mismatch")
		}
	}
	return e, alerts
}

func cmdRetag(args []string) error {
	fs := flag.NewFlagSet("retag", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository, e.g. ghcr.io/owner/name")
	digest := fs.String("digest", "", "existing manifest or index digest")
	tag := fs.String("tag", "", "tag to point at the digest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || !strings.HasPrefix(*digest, "sha256:") || *tag == "" {
		return errors.New("--repo, --digest sha256:... and --tag are required")
	}
	return registry.Retag(*repo, *digest, *tag)
}

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ctyl/internal/evaluate"
	"ctyl/internal/policy"
	"ctyl/internal/registry"
)

// readState returns the recorded state at path; os.ErrNotExist when there is
// none yet.
func readState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// admitRecord decides whether next may replace prev in the release state.
// A retried or re-run job must not roll the record back to an older
// evaluation, nor record a promotion the floating tag (liveDigest) no
// longer reflects: deployments refuse a state that disagrees with the
// registry.
func admitRecord(prev, next State, liveDigest string) error {
	if prev.GateRunID > next.GateRunID {
		return &staleRecord{fmt.Sprintf("%s: recorded state comes from run %d, newer than this record's run %d", next.Tag, prev.GateRunID, next.GateRunID)}
	}
	if next.Promoted && liveDigest != next.Digest {
		return &staleRecord{fmt.Sprintf("%s: the registry tag points at %s, not this record's %s", next.Tag, liveDigest, next.Digest)}
	}
	return nil
}

func cmdAcceptState(args []string) error {
	fs := flag.NewFlagSet("accept-state", flag.ContinueOnError)
	record := fs.String("record", "", "new state JSON (from `ctyl metadata`)")
	current := fs.String("current", "", "recorded state JSON it would replace (may not exist)")
	repo := fs.String("registry", "", "image repository whose floating tag must point at a promoted record's digest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	next, err := readState(*record)
	if err != nil {
		return err
	}
	prev, err := readState(*current)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	live := ""
	if next.Promoted {
		if *repo == "" {
			return errors.New("--registry is required to accept a promoted record")
		}
		m, err := registry.Get(*repo, next.Tag)
		if err != nil {
			return fmt.Errorf("registry %s:%s: %w", *repo, next.Tag, err)
		}
		live = m.Digest
	}
	return admitRecord(prev, next, live)
}

// retirementDue lists the lines past their security support date whose
// recorded state does not say so yet. No pipeline runs for these lines any
// more, so nothing else would change their recorded status.
func retirementDue(p *policy.Policy, stateDir string, now time.Time) ([]policy.Line, error) {
	var due []policy.Line
	for _, l := range p.Lines {
		if l.Supported(now) {
			continue
		}
		s, err := readState(filepath.Join(stateDir, l.Tag+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s.Status != evaluate.StatusUnsupported {
			due = append(due, l)
		}
	}
	return due, nil
}

func supportEndedReason(l policy.Line) string {
	return fmt.Sprintf("Java %d passed its security support date %s", l.Major, l.SecuritySupportUntil.Format("2006-01-02"))
}

func cmdRetire(args []string) error {
	fs := flag.NewFlagSet("retire", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	stateDir := fs.String("state", "state", "release-state directory (updated in place)")
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
	due, err := retirementDue(p, *stateDir, now)
	if err != nil {
		return err
	}
	for _, l := range due {
		path := filepath.Join(*stateDir, l.Tag+".json")
		s, err := readState(path)
		if err != nil {
			return err
		}
		s.Status, s.Reasons, s.CheckedAt = evaluate.StatusUnsupported, []string{supportEndedReason(l)}, now.Format(time.RFC3339)
		if err := writeJSON(path, s); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: marked unsupported\n", l.Tag)
	}
	return nil
}

func cmdDigest(args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository, e.g. ghcr.io/owner/name")
	tag := fs.String("tag", "", "tag to resolve")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *tag == "" {
		return errors.New("--repo and --tag are required")
	}
	m, err := registry.Get(*repo, *tag)
	if errors.Is(err, registry.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Digest == "" {
		return fmt.Errorf("%s:%s: registry reported no digest", *repo, *tag)
	}
	fmt.Println(m.Digest)
	return nil
}

func cmdPolicyDigest(args []string) error {
	fs := flag.NewFlagSet("policy-digest", flag.ContinueOnError)
	var c commonFlags
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(filepath.Join(c.root, c.policyDir))
	if err != nil {
		return err
	}
	fmt.Println(p.Digest)
	return nil
}

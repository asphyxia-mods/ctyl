// Command ctyl is the release tooling: it renders image definitions, fetches
// and verifies vendor JDKs, runs the security gate and plans rebuilds.
// It never runs inside the published images.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type command struct {
	name, usage string
	run         func(args []string) error
}

var commands = []command{
	{"lines", "print the built Java lines as JSON (for CI matrices)", cmdLines},
	{"candidates", "print the distributions to try, in policy order", cmdCandidates},
	{"render", "render the apko config for a line and distribution", cmdRender},
	{"vendor-fetch", "download and verify a vendor JDK and render its melange recipe", cmdVendorFetch},
	{"hygiene", "statically check an image tarball for attack-surface regressions", cmdHygiene},
	{"gate", "evaluate an image tarball against the security policy", cmdGate},
	{"inputs-digest", "compute the content-input digest of a build", cmdInputsDigest},
	{"metadata", "produce build metadata and OCI annotations for a gated image", cmdMetadata},
	{"mark", "record a gate rejection in a line's published state", cmdMark},
	{"accept-state", "check that a new state record may replace the recorded one", cmdAcceptState},
	{"retire", "mark lines past their security support date unsupported in the release state", cmdRetire},
	{"plan", "decide which lines need a rebuild", cmdPlan},
	{"retag", "point a tag at an existing digest without rewriting the manifest", cmdRetag},
	{"digest", "print the digest a tag points at (nothing if the tag does not exist)", cmdDigest},
	{"policy-digest", "print the digest of a security policy directory", cmdPolicyDigest},
	{"upstream", "report the newest upstream GA release of each line", cmdUpstream},
	{"status", "render a status summary from gate reports", cmdStatus},
}

// exitNotPromotable distinguishes "gate said no" from tool failures.
const exitNotPromotable = 3

type gateRejected struct{ msg string }

func (e *gateRejected) Error() string { return e.msg }

// exitStale means a record was not applied because newer state exists.
const exitStale = 4

type staleRecord struct{ msg string }

func (e *staleRecord) Error() string { return e.msg }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	for _, c := range commands {
		if c.name == os.Args[1] {
			err := c.run(os.Args[2:])
			var rej *gateRejected
			var stale *staleRecord
			switch {
			case errors.As(err, &rej):
				fmt.Fprintln(os.Stderr, "ctyl:", err)
				os.Exit(exitNotPromotable)
			case errors.As(err, &stale):
				fmt.Fprintln(os.Stderr, "ctyl:", err)
				os.Exit(exitStale)
			case errors.Is(err, flag.ErrHelp):
				os.Exit(2)
			case err != nil:
				fmt.Fprintln(os.Stderr, "ctyl:", err)
				os.Exit(1)
			}
			return
		}
	}
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ctyl <command> [flags]\n\ncommands:")
	sorted := append([]command(nil), commands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, c := range sorted {
		fmt.Fprintf(os.Stderr, "  %-14s %s\n", c.name, c.usage)
	}
}

type commonFlags struct {
	policyDir string
	root      string
	gitCache  string
	now       string
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.policyDir, "policy", "policy", "policy directory")
	fs.StringVar(&c.root, "root", ".", "repository root (for keys and image definitions)")
	fs.StringVar(&c.gitCache, "git-cache", ".cache/git", "directory for treeless git clones")
	fs.StringVar(&c.now, "now", "", "evaluation time (RFC 3339); default: current time. For regression tests only")
}

func (c *commonFlags) time() (time.Time, error) {
	if c.now == "" {
		return time.Now().UTC(), nil
	}
	return time.Parse(time.RFC3339, c.now)
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

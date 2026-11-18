// Command ctyl-deploy finds servers running outdated ctyl images and rolls
// verified images out through the panel API, gracefully and with rollback.
//
//	ctyl-deploy status   --repo ghcr.io/OWNER/ctyl --state-url URL            (on a node)
//	ctyl-deploy prefetch --repo ghcr.io/OWNER/ctyl --state-url URL            (on a node)
//	ctyl-deploy rollout  --panel pterodactyl --url https://panel ... [--yes]  (anywhere; --node on a node)
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"ctyl/internal/deploy"
	"ctyl/internal/registry"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ctyl-deploy status|prefetch|rollout [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "status":
		err = status(os.Args[2:])
	case "prefetch":
		err = prefetch(os.Args[2:])
	case "rollout":
		err = rollout(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctyl-deploy:", err)
		os.Exit(1)
	}
}

type stateFlags struct {
	repo, stateURL, stateDir string
	tags                     string
	verifyIdentity           string
}

func (s *stateFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.repo, "repo", "", "image repository, e.g. ghcr.io/OWNER/ctyl")
	fs.StringVar(&s.stateURL, "state-url", "", "base URL of the release-state directory (e.g. https://raw.githubusercontent.com/OWNER/REPO/release-state/state)")
	fs.StringVar(&s.stateDir, "state-dir", "", "local checkout of the release-state directory (alternative to --state-url)")
	fs.StringVar(&s.tags, "tags", "java_8,java_11,java_17,java_21,java_25,java_27", "tags to consider")
	fs.StringVar(&s.verifyIdentity, "verify-identity", "", "if set, run `cosign verify` with this certificate identity regexp before deploying")
}

func (s *stateFlags) read(name string) ([]byte, error) {
	if s.stateDir != "" {
		return os.ReadFile(filepath.Join(s.stateDir, name))
	}
	if !strings.HasPrefix(s.stateURL, "https://") {
		return nil, errors.New("--state-url must be https (or use --state-dir)")
	}
	resp, err := http.Get(strings.TrimRight(s.stateURL, "/") + "/" + name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, os.ErrNotExist
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// load reads the release state and cross-checks every target digest against
// the registry, so a stale or tampered state file cannot select an image the
// tag does not point at.
func (s *stateFlags) load() (map[string]deploy.Target, []deploy.HistoryRecord, error) {
	if s.repo == "" {
		return nil, nil, errors.New("--repo is required")
	}
	targets := map[string]deploy.Target{}
	for _, tag := range strings.Split(s.tags, ",") {
		data, err := s.read(tag + ".json")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var t deploy.Target
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, nil, fmt.Errorf("%s.json: %w", tag, err)
		}
		if t.Promoted {
			m, err := registry.Get(s.repo, tag)
			if err != nil {
				return nil, nil, fmt.Errorf("registry %s:%s: %w", s.repo, tag, err)
			}
			if m.Digest != t.Digest {
				return nil, nil, fmt.Errorf("%s:%s is %s in the registry but %s in the release state; refusing to continue", s.repo, tag, m.Digest, t.Digest)
			}
		}
		if s.verifyIdentity != "" {
			out, err := exec.Command("cosign", "verify", s.repo+"@"+t.Digest, "--certificate-identity-regexp", s.verifyIdentity,
				"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com").CombinedOutput()
			if err != nil {
				return nil, nil, fmt.Errorf("signature verification of %s failed: %v\n%s", t.Digest, err, out)
			}
		}
		targets[tag] = t
	}
	var history []deploy.HistoryRecord
	if data, err := s.read("latency.jsonl"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() {
			var h deploy.HistoryRecord
			if json.Unmarshal(sc.Bytes(), &h) == nil {
				history = append(history, h)
			}
		}
	}
	return targets, history, nil
}

// nodePanel lets `status` and `prefetch` run without panel credentials: the
// node's containers are the server list.
type nodePanel struct{ node *deploy.Node }

func (n nodePanel) Name() string { return "node" }
func (n nodePanel) Servers() ([]deploy.Server, error) {
	rs, err := n.node.Servers()
	var out []deploy.Server
	for _, r := range rs {
		out = append(out, deploy.Server{ID: r.UUID, UUID: r.UUID, Name: r.UUID, Image: r.ConfiguredRef})
	}
	return out, err
}
func (nodePanel) SetImage(string, string) error              { return deploy.ErrUnsupported }
func (nodePanel) Power(string, string) error                 { return deploy.ErrUnsupported }
func (nodePanel) State(string) (string, error)               { return "", deploy.ErrUnsupported }
func (nodePanel) Backup(string, string, time.Duration) error { return deploy.ErrUnsupported }

func printPlan(items []deploy.Item) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER\tTAG\tRUNNING\tTARGET\tKIND\tREASON")
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", it.Server.Name, it.Tag, shortDigest(it.CurrentDigest), shortDigest(it.Target.Digest), it.Kind, it.Reason)
	}
	w.Flush()
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	if d == "" {
		return "-"
	}
	return d
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var s stateFlags
	s.register(fs)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets, history, err := s.load()
	if err != nil {
		return err
	}
	items, err := deploy.Plan(deploy.Options{Panel: nodePanel{deploy.NewNode()}, Node: deploy.NewNode(), Repo: s.repo, Targets: targets, History: history})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(items)
	}
	printPlan(items)
	return nil
}

func prefetch(args []string) error {
	fs := flag.NewFlagSet("prefetch", flag.ContinueOnError)
	var s stateFlags
	s.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets, _, err := s.load()
	if err != nil {
		return err
	}
	node := deploy.NewNode()
	for tag, t := range targets {
		if !t.Promoted || t.Status != "verified" {
			fmt.Printf("%s: skipped (%s, promoted=%v)\n", tag, t.Status, t.Promoted)
			continue
		}
		if err := node.Pull(s.repo, tag, t.Digest); err != nil {
			return err
		}
		fmt.Printf("%s: %s@%s pulled and tagged\n", tag, s.repo, t.Digest)
	}
	return nil
}

func rollout(args []string) error {
	fs := flag.NewFlagSet("rollout", flag.ContinueOnError)
	var s stateFlags
	s.register(fs)
	panelType := fs.String("panel", "pterodactyl", "pterodactyl | calagopus")
	panelURL := fs.String("url", "", "panel base URL (https)")
	tokenFile := fs.String("token-file", "", "file containing a client API key (or set CTYL_PANEL_TOKEN)")
	onNode := fs.Bool("node", false, "this host is the panel node: pre-pull images and verify running digests via Docker")
	servers := fs.String("servers", "", "comma-separated server names or identifiers to update (default: all outdated)")
	onlySecurity := fs.Bool("only-security", false, "skip maintenance-only updates")
	windowSpec := fs.String("window", "", `maintenance window, e.g. "Sat,Sun 04:00-06:00" (outside it nothing is restarted)`)
	tz := fs.String("timezone", "UTC", "time zone for --window")
	yes := fs.Bool("yes", false, "apply without interactive confirmation (for scheduled, explicitly enabled automatic deployments)")
	backup := fs.Bool("backup", false, "take a panel backup before each update (Pterodactyl)")
	rollbackImage := fs.String("rollback-image", "", "image the egg offers to roll back to when using mutable tags")
	stopTimeout := fs.Duration("stop-timeout", 10*time.Minute, "how long to wait for a graceful stop (never forces a kill)")
	readyTimeout := fs.Duration("ready-timeout", 15*time.Minute, "how long to wait for the server to report running")
	report := fs.String("report", "", "write JSON results here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token := os.Getenv("CTYL_PANEL_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	var panel deploy.Panel
	var err error
	switch *panelType {
	case "pterodactyl":
		panel, err = deploy.NewPterodactyl(*panelURL, token)
	case "calagopus":
		panel, err = deploy.NewCalagopus(*panelURL, token)
	default:
		err = fmt.Errorf("unknown panel %q", *panelType)
	}
	if err != nil {
		return err
	}
	targets, history, err := s.load()
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return err
	}
	opts := deploy.Options{Panel: panel, Repo: s.repo, Targets: targets, History: history, OnlySecurity: *onlySecurity,
		Backup: *backup, RollbackImage: *rollbackImage, StopTimeout: *stopTimeout, ReadyTimeout: *readyTimeout,
		Now: time.Now, Log: os.Stderr}
	if *onNode {
		opts.Node = deploy.NewNode()
	}
	if *windowSpec != "" {
		if opts.Window, err = deploy.ParseWindow(*windowSpec, loc); err != nil {
			return err
		}
	}
	items, err := deploy.Plan(opts)
	if err != nil {
		return err
	}
	printPlan(items)
	selected := map[string]bool{}
	for _, name := range strings.Split(*servers, ",") {
		if name != "" {
			selected[name] = true
		}
	}
	var todo []deploy.Item
	for _, it := range items {
		if len(selected) > 0 && !selected[it.Server.Name] && !selected[it.Server.ID] {
			continue
		}
		if opts.Wanted(it) {
			todo = append(todo, it)
		}
	}
	if len(todo) == 0 {
		fmt.Println("nothing to update")
		return nil
	}
	if !*yes {
		fmt.Printf("\n%d server(s) will be stopped gracefully, updated and restarted. Type 'update' to continue: ", len(todo))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "update" {
			return errors.New("aborted")
		}
	}
	var results []deploy.Result
	failed := 0
	for _, it := range todo {
		r := deploy.Apply(opts, it)
		results = append(results, r)
		if r.Outcome == "failed" || r.Outcome == "rolled-back" {
			failed++
			// One server at a time; stop the rollout on the first problem.
			break
		}
	}
	if *report != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*report, data, 0o644); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("rollout stopped after a failed update; see the log above")
	}
	return nil
}

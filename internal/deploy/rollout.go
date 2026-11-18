package deploy

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Target is the verified image a tag currently points at, from the
// release-state records.
type Target struct {
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	Promoted   bool   `json:"promoted"`
	UpstreamGA string `json:"upstream_latest_ga"`
	Runtime    string `json:"runtime_version"`
}

// HistoryRecord is one line of release-state latency.jsonl.
type HistoryRecord struct {
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	UpstreamGA string `json:"upstream_latest_ga"`
	Runtime    string `json:"runtime_version"`
}

// NodeAPI is the node-side functionality (implemented by *Node).
type NodeAPI interface {
	Servers() ([]RunningServer, error)
	Pull(repo, tag, digest string) error
	ContainerDigest(uuid, repo string) (string, error)
}

const (
	KindCurrent      = "current"
	KindJavaSecurity = "java-security" // newer OpenJDK update release (every update is a security release)
	KindSecurity     = "security"      // running digest was recorded as not verified
	KindMaintenance  = "maintenance"   // userspace/package or launcher changes
	KindUnknown      = "unknown"       // running digest unknown (no node access, mutable tag)
	KindOtherNode    = "other-node"    // --node rollout, but the server's container is not on this node
)

type Item struct {
	Server        Server `json:"server"`
	Tag           string `json:"tag"`
	Pinned        bool   `json:"pinned"`
	CurrentDigest string `json:"current_digest,omitempty"`
	Target        Target `json:"target"`
	Kind          string `json:"kind"`
	Reason        string `json:"reason"`
}

type Options struct {
	Panel   Panel
	Node    NodeAPI // nil when not running on the node
	Repo    string  // e.g. ghcr.io/owner/ctyl
	Targets map[string]Target
	History []HistoryRecord

	OnlySecurity    bool
	AllowUnverified bool
	Window          *Window
	Backup          bool
	RollbackImage   string // tag-mode rollback reference (must be offered by the egg)
	StopTimeout     time.Duration
	ReadyTimeout    time.Duration
	Now             func() time.Time
	Log             io.Writer
}

var refRE = regexp.MustCompile(`^([^@]+?)(?::([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}))?(?:@(sha256:[0-9a-f]{64}))?$`)

// ParseRef splits "repo:tag@digest" (tag and digest optional).
func ParseRef(ref string) (repo, tag, digest string, ok bool) {
	m := refRE.FindStringSubmatch(ref)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// Plan classifies every panel server that uses an image from opts.Repo.
func Plan(opts Options) ([]Item, error) {
	servers, err := opts.Panel.Servers()
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	running := map[string]RunningServer{}
	if opts.Node != nil {
		rs, err := opts.Node.Servers()
		if err != nil {
			return nil, fmt.Errorf("listing containers on this node: %w", err)
		}
		for _, r := range rs {
			running[r.UUID] = r
		}
	}
	history := map[string]HistoryRecord{}
	for _, h := range opts.History {
		history[h.Digest] = h
	}

	var items []Item
	for _, s := range servers {
		repo, tag, pinned, ok := ParseRef(s.Image)
		if !ok || repo != opts.Repo || tag == "" {
			continue
		}
		it := Item{Server: s, Tag: tag, Pinned: pinned != "", CurrentDigest: pinned}
		target, ok := opts.Targets[tag]
		if !ok {
			it.Kind, it.Reason = KindUnknown, "no release state for tag "+tag
			items = append(items, it)
			continue
		}
		it.Target = target
		local, onThisNode := running[s.UUID]
		if opts.Node != nil && !onThisNode {
			it.Kind, it.Reason = KindOtherNode, "no container on this node; run the rollout on the node that hosts it"
			items = append(items, it)
			continue
		}
		running := ""
		if onThisNode {
			running = local.Digest(opts.Repo)
		}
		it.CurrentDigest = outdatedDigest(target.Digest, running, pinned)
		switch {
		case it.CurrentDigest == "":
			it.Kind, it.Reason = KindUnknown, "running digest unknown (mutable tag; run on the node to inspect containers)"
		case it.CurrentDigest == target.Digest:
			it.Kind, it.Reason = KindCurrent, "running the current verified digest"
		default:
			prev, known := history[it.CurrentDigest]
			prevJava, targetJava := javaVersion(prev.Runtime), javaVersion(target.Runtime)
			switch {
			case !known:
				it.Kind, it.Reason = KindSecurity, "running digest is not a recorded release; treat as outdated"
			case prevJava == "" || targetJava == "":
				it.Kind, it.Reason = KindSecurity, "Java version of the running or target digest is not recorded; treat as outdated"
			case prevJava != targetJava:
				it.Kind, it.Reason = KindJavaSecurity, fmt.Sprintf("Java update %s -> %s", prevJava, targetJava)
			case prev.Status != "verified":
				it.Kind, it.Reason = KindSecurity, "running digest was recorded as "+prev.Status
			default:
				it.Kind, it.Reason = KindMaintenance, "userspace or launcher update"
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// outdatedDigest picks the digest to classify a server by: the first known
// one that differs from target, so a server counts as current only if both
// the container it runs and the panel pin it restarts into are the target.
// A pin changed ahead of a restart must not hide an old running container.
func outdatedDigest(target string, known ...string) string {
	first := ""
	for _, d := range known {
		switch {
		case d == "":
		case d != target:
			return d
		case first == "":
			first = d
		}
	}
	return first
}

// javaVersion is the OpenJDK version an image ships, without the build and
// packaging suffix: "21.0.13.1+-wolfi-r0" -> "21.0.13.1". Images are compared
// by what they contain, not by the upstream release known when they were built.
func javaVersion(runtime string) string {
	version, _, _ := strings.Cut(runtime, "+")
	return version
}

// Wanted reports whether a rollout should touch the item under opts.
func (o Options) Wanted(it Item) bool {
	switch it.Kind {
	case KindCurrent, KindUnknown, KindOtherNode:
		return false
	case KindMaintenance:
		return !o.OnlySecurity
	}
	return true
}

type Result struct {
	Item       Item   `json:"item"`
	Outcome    string `json:"outcome"` // updated | skipped | failed | rolled-back
	Detail     string `json:"detail"`
	Deployed   string `json:"deployed_digest,omitempty"`
	FinishedAt string `json:"finished_at"`
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", args...)
	}
}

// Apply updates one server: optional backup, graceful stop (never forced),
// image switch, start, readiness wait, digest verification, rollback.
func Apply(o Options, it Item) Result {
	r := Result{Item: it}
	done := func(outcome, format string, args ...any) Result {
		r.Outcome, r.Detail, r.FinishedAt = outcome, fmt.Sprintf(format, args...), o.Now().UTC().Format(time.RFC3339)
		o.logf("[%s] %s: %s", it.Server.Name, outcome, r.Detail)
		return r
	}
	outsideWindow := func() bool { return o.Window != nil && !o.Window.Contains(o.Now()) }
	if outsideWindow() {
		return done("skipped", "outside maintenance window %s", o.Window)
	}
	if !o.AllowUnverified && (it.Target.Status != "verified" || !it.Target.Promoted) {
		return done("skipped", "target %s is %s (promoted=%v); refusing to deploy an unverified image", short(it.Target.Digest), it.Target.Status, it.Target.Promoted)
	}
	targetRef := fmt.Sprintf("%s:%s@%s", o.Repo, it.Tag, it.Target.Digest)

	if o.Node != nil {
		// Every later step verifies through this node's Docker daemon; a
		// server hosted elsewhere must not be stopped or restarted from here.
		if _, err := o.Node.ContainerDigest(it.Server.UUID, o.Repo); err != nil {
			return done("skipped", "not on this node: %v (server untouched)", err)
		}
		o.logf("[%s] pulling %s", it.Server.Name, targetRef)
		if err := o.Node.Pull(o.Repo, it.Tag, it.Target.Digest); err != nil {
			return done("failed", "pre-pull: %v (server untouched)", err)
		}
	}
	if o.Backup {
		o.logf("[%s] creating backup", it.Server.Name)
		if err := o.Panel.Backup(it.Server.ID, "ctyl pre-update "+short(it.Target.Digest), 30*time.Minute); err != nil {
			return done("failed", "backup: %v (server untouched)", err)
		}
	}
	if outsideWindow() {
		return done("skipped", "maintenance window %s closed during preparation (server untouched)", o.Window)
	}
	if err := o.stop(it.Server); err != nil {
		return done("failed", "%v", err)
	}
	if it.Pinned {
		if err := o.Panel.SetImage(it.Server.ID, targetRef); err != nil {
			o.startQuietly(it.Server)
			return done("failed", "setting image: %v (restarted with the previous image)", err)
		}
	}
	err := o.startAndVerify(it.Server, it.Target.Digest)
	if err == nil {
		r.Deployed = it.Target.Digest
		return done("updated", "now running %s (%s)", short(it.Target.Digest), it.Target.Runtime)
	}

	rollback := o.RollbackImage
	if it.Pinned {
		// The panel pin may already name the target (changed ahead of a
		// restart); roll back to what the server actually ran.
		rollback = fmt.Sprintf("%s:%s@%s", o.Repo, it.Tag, it.CurrentDigest)
	}
	if rollback == "" {
		if serr := o.stop(it.Server); serr != nil {
			return done("failed", "%v; no rollback image configured and stopping for inspection failed: %v", err, serr)
		}
		return done("failed", "%v; no rollback image configured, server left stopped for inspection", err)
	}
	o.logf("[%s] rolling back to %s after: %v", it.Server.Name, rollback, err)
	if serr := o.stop(it.Server); serr != nil {
		return done("failed", "%v; rollback stop failed: %v", err, serr)
	}
	if serr := o.Panel.SetImage(it.Server.ID, rollback); serr != nil {
		return done("failed", "%v; rollback image switch failed: %v", err, serr)
	}
	_, _, prevDigest, _ := ParseRef(rollback)
	if rerr := o.startAndVerify(it.Server, prevDigest); rerr != nil {
		return done("failed", "%v; rollback did not become ready: %v", err, rerr)
	}
	return done("rolled-back", "update failed (%v); restored %s", err, rollback)
}

func (o Options) stop(s Server) error {
	state, err := o.Panel.State(s.ID)
	if err != nil {
		return fmt.Errorf("reading state: %w", err)
	}
	if state == "offline" {
		return nil
	}
	o.logf("[%s] stopping gracefully (egg stop command)", s.Name)
	if err := o.Panel.Power(s.ID, "stop"); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := o.waitState(s, "offline", o.StopTimeout); err != nil {
		return fmt.Errorf("server did not stop within %s; not forcing a kill to protect world data: %w", o.StopTimeout, err)
	}
	return nil
}

func (o Options) startAndVerify(s Server, digest string) error {
	if err := o.Panel.Power(s.ID, "start"); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := o.waitState(s, "running", o.ReadyTimeout); err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	if o.Node != nil && digest != "" {
		got, err := o.Node.ContainerDigest(s.UUID, o.Repo)
		if err != nil {
			return fmt.Errorf("verifying container image: %w", err)
		}
		if got != digest {
			return fmt.Errorf("container runs %s, expected %s", short(got), short(digest))
		}
	}
	return nil
}

func (o Options) startQuietly(s Server) {
	if err := o.Panel.Power(s.ID, "start"); err != nil {
		o.logf("[%s] restart after failure: %v", s.Name, err)
	}
}

func (o Options) waitState(s Server, want string, timeout time.Duration) error {
	started := o.Now()
	deadline := started.Add(timeout)
	last := ""
	for {
		state, err := o.Panel.State(s.ID)
		if err == nil {
			last = state
			if state == want {
				return nil
			}
			// Panels report "offline" briefly before a start is processed;
			// still offline after that grace period means the server crashed.
			if want == "running" && state == "offline" && o.Now().Sub(started) > startGrace {
				return fmt.Errorf("server went offline (crashed during start)")
			}
		}
		if o.Now().After(deadline) {
			return fmt.Errorf("still %q after %s", last, timeout)
		}
		time.Sleep(pollInterval)
	}
}

var startGrace = 30 * time.Second

func short(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Window is a recurring maintenance window such as "Sat,Sun 04:00-06:00".
type Window struct {
	Days       map[time.Weekday]bool // empty = every day
	Start, End int                   // minutes after midnight
	Loc        *time.Location
	spec       string
}

var dayNames = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

func ParseWindow(spec string, loc *time.Location) (*Window, error) {
	w := &Window{Days: map[time.Weekday]bool{}, Loc: loc, spec: spec}
	fields := strings.Fields(spec)
	if len(fields) == 2 {
		for _, d := range strings.Split(strings.ToLower(fields[0]), ",") {
			wd, ok := dayNames[d]
			if !ok {
				return nil, fmt.Errorf("unknown day %q", d)
			}
			w.Days[wd] = true
		}
		fields = fields[1:]
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("window %q: expected [DAYS] HH:MM-HH:MM", spec)
	}
	var sh, sm, eh, em int
	if _, err := fmt.Sscanf(fields[0], "%d:%d-%d:%d", &sh, &sm, &eh, &em); err != nil || sh > 23 || eh > 24 || sm > 59 || em > 59 {
		return nil, fmt.Errorf("window %q: invalid time range", spec)
	}
	w.Start, w.End = sh*60+sm, eh*60+em
	return w, nil
}

func (w *Window) Contains(t time.Time) bool {
	t = t.In(w.Loc)
	m := t.Hour()*60 + t.Minute()
	day := t.Weekday()
	if w.Start <= w.End {
		return (len(w.Days) == 0 || w.Days[day]) && m >= w.Start && m < w.End
	}
	// Overnight window: the part after midnight belongs to the previous day.
	if m >= w.Start {
		return len(w.Days) == 0 || w.Days[day]
	}
	return m < w.End && (len(w.Days) == 0 || w.Days[(day+6)%7])
}

func (w *Window) String() string { return w.spec + " " + w.Loc.String() }

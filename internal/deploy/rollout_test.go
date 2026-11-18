package deploy

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	repo = "ghcr.io/example/ctyl"
	dOld = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	dNew = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// fakePanel simulates a panel daemon: start/stop take effect on the next
// State call, and a start can be made to crash.
type fakePanel struct {
	servers   []Server
	state     map[string]string
	pending   map[string]string
	images    map[string]string
	calls     []string
	crash     bool
	slowStop  bool
	backupErr error
	node      *fakeNode
}

func newFakePanel(servers ...Server) *fakePanel {
	p := &fakePanel{servers: servers, state: map[string]string{}, pending: map[string]string{}, images: map[string]string{}}
	for _, s := range servers {
		p.state[s.ID], p.images[s.ID] = "running", s.Image
	}
	return p
}

func (p *fakePanel) Name() string { return "fake" }
func (p *fakePanel) Servers() ([]Server, error) {
	out := append([]Server(nil), p.servers...)
	for i := range out {
		out[i].Image = p.images[out[i].ID]
	}
	return out, nil
}
func (p *fakePanel) SetImage(id, image string) error {
	p.calls = append(p.calls, "image "+image)
	p.images[id] = image
	return nil
}
func (p *fakePanel) Power(id, action string) error {
	p.calls = append(p.calls, action)
	switch action {
	case "stop":
		if !p.slowStop {
			p.pending[id] = "offline"
		}
	case "start":
		if p.crash && strings.Contains(p.images[id], dNew) || p.crash && !strings.Contains(p.images[id], "@") {
			p.pending[id] = "offline"
		} else {
			p.pending[id] = "running"
			if p.node != nil {
				_, _, d, _ := ParseRef(p.images[id])
				if d == "" {
					d = p.node.tagged
				}
				p.node.containers[id] = d
			}
		}
	case "kill":
		panic("rollouts must never kill a server")
	}
	return nil
}
func (p *fakePanel) State(id string) (string, error) {
	if s, ok := p.pending[id]; ok {
		p.state[id] = s
		delete(p.pending, id)
	}
	return p.state[id], nil
}
func (p *fakePanel) Backup(id, name string, _ time.Duration) error {
	p.calls = append(p.calls, "backup")
	return p.backupErr
}

type fakeNode struct {
	containers map[string]string
	tagged     string
	pullErr    error
}

func (n *fakeNode) Servers() ([]RunningServer, error) {
	var out []RunningServer
	for id, d := range n.containers {
		out = append(out, RunningServer{UUID: id, RepoDigests: []string{repo + "@" + d}})
	}
	return out, nil
}
func (n *fakeNode) Pull(_, _, digest string) error {
	if n.pullErr != nil {
		return n.pullErr
	}
	n.tagged = digest
	return nil
}
func (n *fakeNode) ContainerDigest(uuid, _ string) (string, error) { return n.containers[uuid], nil }

func options(p *fakePanel, n *fakeNode) Options {
	o := Options{
		Panel: p, Repo: repo,
		Targets:     map[string]Target{"java_21": {Tag: "java_21", Digest: dNew, Status: "verified", Promoted: true, UpstreamGA: "jdk-21.0.13.1-ga"}},
		History:     []HistoryRecord{{Tag: "java_21", Digest: dOld, Status: "verified", UpstreamGA: "jdk-21.0.13-ga"}},
		StopTimeout: time.Second, ReadyTimeout: time.Second,
		Now: time.Now,
	}
	if n != nil {
		o.Node = n
	}
	return o
}

func init() {
	pollInterval = time.Millisecond
	startGrace = 0
}

func TestPlanClassifiesServers(t *testing.T) {
	p := newFakePanel(
		Server{ID: "a", UUID: "a", Name: "pinned-old", Image: repo + ":java_21@" + dOld},
		Server{ID: "b", UUID: "b", Name: "pinned-new", Image: repo + ":java_21@" + dNew},
		Server{ID: "c", UUID: "c", Name: "tag-unknown", Image: repo + ":java_21"},
		Server{ID: "d", UUID: "d", Name: "other-image", Image: "ghcr.io/pterodactyl/yolks:java_21"},
		Server{ID: "e", UUID: "e", Name: "unrecorded", Image: repo + ":java_21@sha256:" + strings.Repeat("3", 64)},
	)
	items, err := Plan(options(p, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Server.Name] = it.Kind
	}
	want := map[string]string{"pinned-old": KindJavaSecurity, "pinned-new": KindCurrent, "tag-unknown": KindUnknown, "unrecorded": KindSecurity}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: kind %q want %q", k, got[k], v)
		}
	}
	if _, ok := got["other-image"]; ok {
		t.Error("foreign image included")
	}
}

func TestPlanUsesNodeForMutableTags(t *testing.T) {
	p := newFakePanel(Server{ID: "c", UUID: "c", Name: "tag", Image: repo + ":java_21"})
	items, _ := Plan(options(p, &fakeNode{containers: map[string]string{"c": dOld}}))
	if items[0].Kind != KindJavaSecurity || items[0].CurrentDigest != dOld {
		t.Errorf("%+v", items[0])
	}
}

func TestApplyPinnedSuccess(t *testing.T) {
	p := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	n := &fakeNode{containers: map[string]string{"a": dOld}}
	p.node = n
	o := options(p, n)
	o.Backup = true
	items, _ := Plan(o)
	r := Apply(o, items[0])
	if r.Outcome != "updated" || r.Deployed != dNew {
		t.Fatalf("%+v", r)
	}
	if want := "backup stop image " + repo + ":java_21@" + dNew + " start"; strings.Join(p.calls, " ") != want {
		t.Errorf("calls %q want %q", p.calls, want)
	}
}

func TestApplyTagModePrePullsAndVerifies(t *testing.T) {
	p := newFakePanel(Server{ID: "c", UUID: "c", Name: "s", Image: repo + ":java_21"})
	n := &fakeNode{containers: map[string]string{"c": dOld}}
	p.node = n
	o := options(p, n)
	items, _ := Plan(o)
	r := Apply(o, items[0])
	if r.Outcome != "updated" || n.tagged != dNew {
		t.Fatalf("%+v tagged=%s", r, n.tagged)
	}
}

func TestApplyRollsBackPinnedOnCrash(t *testing.T) {
	p := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	p.crash = true
	o := options(p, nil)
	items, _ := Plan(o)
	r := Apply(o, items[0])
	if r.Outcome != "rolled-back" || p.images["a"] != repo+":java_21@"+dOld || p.state["a"] != "running" {
		t.Fatalf("%+v image=%s state=%s", r, p.images["a"], p.state["a"])
	}
}

func TestApplyNeverForcesAStop(t *testing.T) {
	p := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	p.slowStop = true
	o := options(p, nil)
	items, _ := Plan(o)
	r := Apply(o, items[0])
	if r.Outcome != "failed" || !strings.Contains(r.Detail, "not forcing a kill") {
		t.Fatalf("%+v", r)
	}
	for _, c := range p.calls {
		if c == "kill" || strings.HasPrefix(c, "image") {
			t.Errorf("unexpected call %s", c)
		}
	}
}

func TestApplyRefusesUnverifiedTargets(t *testing.T) {
	p := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	o := options(p, nil)
	o.Targets["java_21"] = Target{Tag: "java_21", Digest: dNew, Status: "vulnerable", Promoted: false}
	items, _ := Plan(o)
	if r := Apply(o, items[0]); r.Outcome != "skipped" || len(p.calls) != 0 {
		t.Fatalf("%+v %v", r, p.calls)
	}
}

func TestApplyStopsOnBackupOrPullFailureWithoutTouchingServer(t *testing.T) {
	p := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	p.backupErr = errors.New("disk full")
	o := options(p, nil)
	o.Backup = true
	items, _ := Plan(o)
	if r := Apply(o, items[0]); r.Outcome != "failed" || strings.Join(p.calls, " ") != "backup" {
		t.Fatalf("%+v %v", r, p.calls)
	}
	p2 := newFakePanel(Server{ID: "a", UUID: "a", Name: "s", Image: repo + ":java_21@" + dOld})
	o2 := options(p2, &fakeNode{pullErr: errors.New("unauthorized"), containers: map[string]string{}})
	items, _ = Plan(o2)
	if r := Apply(o2, items[0]); r.Outcome != "failed" || len(p2.calls) != 0 {
		t.Fatalf("%+v %v", r, p2.calls)
	}
}

func TestOnlySecurity(t *testing.T) {
	o := Options{OnlySecurity: true}
	if o.Wanted(Item{Kind: KindMaintenance}) || !o.Wanted(Item{Kind: KindJavaSecurity}) || o.Wanted(Item{Kind: KindCurrent}) {
		t.Error("OnlySecurity filter")
	}
}

func TestWindow(t *testing.T) {
	loc := time.UTC
	w, err := ParseWindow("Sat,Sun 23:00-02:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	sat2330 := time.Date(2026, 11, 21, 23, 30, 0, 0, loc) // Saturday
	sun0130 := time.Date(2026, 11, 22, 1, 30, 0, 0, loc)  // Sunday, part of Saturday's window
	mon0130 := time.Date(2026, 11, 23, 1, 30, 0, 0, loc)  // Monday, part of Sunday's window
	tue0130 := time.Date(2026, 11, 24, 1, 30, 0, 0, loc)
	fri2330 := time.Date(2026, 11, 20, 23, 30, 0, 0, loc)
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{{sat2330, true}, {sun0130, true}, {mon0130, true}, {tue0130, false}, {fri2330, false}} {
		if got := w.Contains(tc.t); got != tc.want {
			t.Errorf("%s: %v want %v", tc.t.Format(time.RFC1123), got, tc.want)
		}
	}
	daily, _ := ParseWindow("04:00-06:00", loc)
	if !daily.Contains(time.Date(2026, 11, 18, 4, 0, 0, 0, loc)) || daily.Contains(time.Date(2026, 11, 18, 6, 0, 0, 0, loc)) {
		t.Error("daily window bounds")
	}
	if _, err := ParseWindow("Funday 04:00-06:00", loc); err == nil {
		t.Error("bad day accepted")
	}
}

func TestParseRef(t *testing.T) {
	r, tag, d, ok := ParseRef(repo + ":java_21@" + dNew)
	if !ok || r != repo || tag != "java_21" || d != dNew {
		t.Errorf("%s %s %s %v", r, tag, d, ok)
	}
	if r, tag, _, _ := ParseRef("localhost:5000/ctyl:java_17"); r != "localhost:5000/ctyl" || tag != "java_17" {
		t.Errorf("registry with port: %s %s", r, tag)
	}
}

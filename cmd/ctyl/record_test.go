package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctyl/internal/evaluate"
	"ctyl/internal/policy"
)

const (
	digestOld = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestNew = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func TestAdmitRecord(t *testing.T) {
	recorded := State{Tag: "java_21", Digest: digestNew, GateRunID: 200, Promoted: true}
	tests := []struct {
		name  string
		prev  State
		next  State
		live  string
		stale bool
	}{
		{"retry of an older run's record", recorded, State{Tag: "java_21", Digest: digestOld, GateRunID: 100, Promoted: true}, digestNew, true},
		{"older run that re-confirmed the live digest", recorded, State{Tag: "java_21", Digest: digestNew, GateRunID: 100, Promoted: true}, digestNew, true},
		{"tag moved on since promotion", State{}, State{Tag: "java_21", Digest: digestOld, GateRunID: 300, Promoted: true}, digestNew, true},
		{"retry of the same run", recorded, recorded, digestNew, false},
		{"newer run", recorded, State{Tag: "java_21", Digest: digestOld, GateRunID: 300, Promoted: true}, digestOld, false},
		{"first record of a line", State{}, State{Tag: "java_21", Digest: digestOld, GateRunID: 1, Promoted: true}, digestOld, false},
		{"legacy record without a run", State{Tag: "java_21", Digest: digestOld, Promoted: true}, recorded, digestNew, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := admitRecord(tt.prev, tt.next, tt.live)
			var stale *staleRecord
			if errors.As(err, &stale) != tt.stale {
				t.Errorf("admitRecord = %v, want stale=%v", err, tt.stale)
			}
		})
	}
}

func TestMarkIgnoresAnOlderRunsRejection(t *testing.T) {
	dir := t.TempDir()
	state, report := filepath.Join(dir, "java_21.json"), filepath.Join(dir, "gate.json")
	writeJSON(state, State{Line: 21, Tag: "java_21", Digest: digestNew, Status: evaluate.StatusVerified, GateRunID: 200})
	writeJSON(report, GateResult{Line: 21, Tag: "java_21", Status: evaluate.StatusVulnerable, RunID: 100})

	err := cmdMark([]string{"--report", report, "--state", state})
	var stale *staleRecord
	if !errors.As(err, &stale) {
		t.Fatalf("cmdMark = %v, want a stale-record error", err)
	}
	if s, _ := readState(state); s.Status != evaluate.StatusVerified || s.GateRunID != 200 {
		t.Errorf("state changed by an older rejection: %+v", s)
	}

	writeJSON(report, GateResult{Line: 21, Tag: "java_21", Status: evaluate.StatusVulnerable, RunID: 300})
	if err := cmdMark([]string{"--report", report, "--state", state}); err != nil {
		t.Fatal(err)
	}
	if s, _ := readState(state); s.Status != evaluate.StatusVulnerable || s.GateRunID != 300 {
		t.Errorf("newer rejection not recorded: %+v", s)
	}
}

func TestRetireMarksLinesPastSupport(t *testing.T) {
	root := "../.."
	p, err := policy.Load(filepath.Join(root, "policy"))
	if err != nil {
		t.Fatal(err)
	}
	ended, _ := p.Line(27)
	supported, _ := p.Line(21)
	now := ended.SecuritySupportUntil.Add(48 * time.Hour)
	if !supported.Supported(now) {
		t.Fatalf("test assumes %s is still supported at %s", supported.Tag, now)
	}
	dir := t.TempDir()
	for _, l := range []policy.Line{ended, supported} {
		writeJSON(filepath.Join(dir, l.Tag+".json"), State{Line: l.Major, Tag: l.Tag, Digest: digestOld, Status: evaluate.StatusVerified, Promoted: true})
	}

	due, err := retirementDue(p, dir, now)
	if err != nil || len(due) != 1 || due[0].Tag != ended.Tag {
		t.Fatalf("retirementDue = %v, %v", due, err)
	}
	if err := cmdRetire([]string{"--root", root, "--state", dir, "--now", now.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	s, _ := readState(filepath.Join(dir, ended.Tag+".json"))
	if s.Status != evaluate.StatusUnsupported || s.Digest != digestOld || !s.Promoted || len(s.Reasons) != 1 || !strings.Contains(s.Reasons[0], "support date") {
		t.Errorf("retired state %+v", s)
	}
	if s, _ := readState(filepath.Join(dir, supported.Tag+".json")); s.Status != evaluate.StatusVerified {
		t.Errorf("supported line changed: %+v", s)
	}
	if due, _ := retirementDue(p, dir, now); len(due) != 0 {
		t.Errorf("retirement not idempotent: %v", due)
	}
}

func TestDigestFailsUnlessTheTagIsConfirmedMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/o/ctyl/manifests/java_21":
			w.Header().Set("Docker-Content-Digest", digestNew)
			w.Write([]byte(`{"mediaType":"application/vnd.oci.image.index.v1+json"}`))
		case "/v2/o/ctyl/manifests/java_8":
			http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/ctyl"

	stdout := os.Stdout
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = devnull
	defer func() { os.Stdout = stdout }()
	for tag, wantErr := range map[string]bool{"java_21": false, "java_8": false, "java_17": true} {
		if err := cmdDigest([]string{"--repo", repo, "--tag", tag}); (err != nil) != wantErr {
			t.Errorf("%s: err %v, want error=%v", tag, err, wantErr)
		}
	}
}

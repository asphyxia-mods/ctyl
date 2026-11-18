package deploy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recorded struct{ method, path, body, auth string }

func fakeAPI(t *testing.T, routes map[string]string) (*httptest.Server, *[]recorded) {
	var calls []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, recorded{r.Method, r.URL.RequestURI(), string(b), r.Header.Get("Authorization")})
		if resp, ok := routes[r.Method+" "+r.URL.RequestURI()]; ok {
			w.Write([]byte(resp))
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestPterodactylClientContract(t *testing.T) {
	srv, calls := fakeAPI(t, map[string]string{
		"GET /api/client?type=admin-all&page=1":       `{"data":[{"attributes":{"identifier":"1a2b3c4d","uuid":"1a2b3c4d-0000","name":"survival","docker_image":"ghcr.io/x/ctyl:java_21"}}],"meta":{"pagination":{"total_pages":1}}}`,
		"GET /api/client/servers/1a2b3c4d/resources":  `{"attributes":{"current_state":"running"}}`,
		"POST /api/client/servers/1a2b3c4d/backups":   `{"attributes":{"uuid":"b1"}}`,
		"GET /api/client/servers/1a2b3c4d/backups/b1": `{"attributes":{"is_successful":true,"completed_at":"2026-11-18T00:00:00Z"}}`,
	})
	p, err := NewPterodactyl(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), "ptlc_secret")
	if err != nil {
		t.Fatal(err)
	}
	servers, err := p.Servers()
	if err != nil || len(servers) != 1 || servers[0].ID != "1a2b3c4d" || servers[0].UUID != "1a2b3c4d-0000" || servers[0].Image != "ghcr.io/x/ctyl:java_21" {
		t.Fatalf("%v %+v", err, servers)
	}
	if st, err := p.State("1a2b3c4d"); err != nil || st != "running" {
		t.Fatalf("state %q %v", st, err)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.Power("1a2b3c4d", "stop"))
	must(p.SetImage("1a2b3c4d", "ghcr.io/x/ctyl:java_21@sha256:ab"))
	must(p.Backup("1a2b3c4d", "pre", time.Second))
	want := []recorded{
		{"POST", "/api/client/servers/1a2b3c4d/power", `{"signal":"stop"}`, "Bearer ptlc_secret"},
		{"PUT", "/api/client/servers/1a2b3c4d/settings/docker-image", `{"docker_image":"ghcr.io/x/ctyl:java_21@sha256:ab"}`, "Bearer ptlc_secret"},
	}
	for _, w := range want {
		found := false
		for _, c := range *calls {
			if c == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call %+v in %+v", w, *calls)
		}
	}
}

func TestCalagopusClientContract(t *testing.T) {
	srv, calls := fakeAPI(t, map[string]string{
		"GET /api/client/servers?page=1&per_page=100":                            `{"servers":{"total":1,"per_page":100,"page":1,"data":[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"modded","image":"ghcr.io/x/ctyl:java_17"}]}}`,
		"GET /api/client/servers/123e4567-e89b-12d3-a456-426614174000/resources": `{"resources":{"state":"offline"}}`,
	})
	p, _ := NewCalagopus(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), "cgp_secret")
	servers, err := p.Servers()
	if err != nil || len(servers) != 1 || servers[0].Image != "ghcr.io/x/ctyl:java_17" {
		t.Fatalf("%v %+v", err, servers)
	}
	id := servers[0].ID
	if st, _ := p.State(id); st != "offline" {
		t.Errorf("state %q", st)
	}
	p.Power(id, "start")
	p.SetImage(id, "ghcr.io/x/ctyl:java_17@sha256:cd")
	var power, image map[string]string
	for _, c := range *calls {
		switch c.path {
		case "/api/client/servers/" + id + "/power":
			json.Unmarshal([]byte(c.body), &power)
		case "/api/client/servers/" + id + "/startup/docker_image":
			json.Unmarshal([]byte(c.body), &image)
		}
	}
	if power["action"] != "start" || image["image"] != "ghcr.io/x/ctyl:java_17@sha256:cd" {
		t.Errorf("power %v image %v", power, image)
	}
	if err := p.Backup(id, "x", time.Second); err != ErrUnsupported {
		t.Errorf("backup: %v", err)
	}
}

func TestPanelURLMustBeHTTPS(t *testing.T) {
	if _, err := NewPterodactyl("http://panel.example.com", "t"); err == nil {
		t.Error("plain http accepted")
	}
	if _, err := NewCalagopus("https://panel.example.com", ""); err == nil {
		t.Error("empty token accepted")
	}
}

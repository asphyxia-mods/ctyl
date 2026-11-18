package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Node talks to the Docker daemon on a panel node (Wings host) to see which
// image digest each server container actually runs.
type Node struct{ http *http.Client }

func NewNode() *Node {
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		sock = strings.TrimPrefix(h, "unix://")
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}
	return &Node{http: &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 20 * time.Minute}}
}

func (n *Node) get(path string, q url.Values, out any) error {
	u := "http://docker/v1.45" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := n.http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker GET %s: %s %s", path, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// RunningServer is a panel-managed server container on this node.
type RunningServer struct {
	UUID          string   `json:"uuid"`
	ConfiguredRef string   `json:"configured_image"`
	ImageID       string   `json:"image_id"`
	RepoDigests   []string `json:"repo_digests"`
	State         string   `json:"state"`
	Java          string   `json:"java,omitempty"`
}

// Digest returns the repository digest of the running image for repo.
func (r RunningServer) Digest(repo string) string {
	for _, d := range r.RepoDigests {
		if strings.HasPrefix(d, repo+"@") {
			return strings.TrimPrefix(d, repo+"@")
		}
	}
	return ""
}

// Servers lists server-process containers (both Pterodactyl Wings and
// Calagopus Wings label them ContainerType=server_process).
func (n *Node) Servers() ([]RunningServer, error) {
	var list []struct {
		ID, Image, ImageID, State string
		Names                     []string
	}
	filters, _ := json.Marshal(map[string][]string{"label": {"ContainerType=server_process"}})
	if err := n.get("/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, &list); err != nil {
		return nil, err
	}
	var out []RunningServer
	for _, c := range list {
		rs := RunningServer{ConfiguredRef: c.Image, ImageID: c.ImageID, State: c.State}
		if len(c.Names) > 0 {
			rs.UUID = strings.TrimPrefix(c.Names[0], "/")
		}
		// The list reports an image ID once the tag has moved on; the
		// container's own config keeps the reference it was created from.
		var ins struct{ Config struct{ Image string } }
		if err := n.get("/containers/"+c.ID+"/json", nil, &ins); err == nil && ins.Config.Image != "" {
			rs.ConfiguredRef = ins.Config.Image
		}
		var img struct {
			RepoDigests []string
			Config      struct{ Env []string }
		}
		if err := n.get("/images/"+c.ImageID+"/json", nil, &img); err == nil {
			rs.RepoDigests = img.RepoDigests
			for _, kv := range img.Config.Env {
				if v, ok := strings.CutPrefix(kv, "CTYL_IMAGE="); ok {
					rs.Java = v
				}
			}
		}
		out = append(out, rs)
	}
	return out, nil
}

// Pull fetches repo@digest and points tag at it locally, so the panel daemon
// starts the verified image even if its own pull is skipped (Calagopus pull
// cache) or fails (both daemons then fall back to the local tag).
func (n *Node) Pull(repo, tag, digest string) error {
	q := url.Values{"fromImage": {repo}, "tag": {digest}}
	req, _ := http.NewRequest(http.MethodPost, "http://docker/v1.45/images/create?"+q.Encode(), nil)
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(body), `"error"`) {
		return fmt.Errorf("pull %s@%s: %s %s", repo, digest, resp.Status, lastLine(body))
	}
	q = url.Values{"repo": {repo}, "tag": {tag}}
	req, _ = http.NewRequest(http.MethodPost, "http://docker/v1.45/images/"+url.PathEscape(repo+"@"+digest)+"/tag?"+q.Encode(), nil)
	resp, err = n.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tag %s:%s: %s", repo, tag, resp.Status)
	}
	return nil
}

// ContainerDigest reports the repository digest the server's current
// container was created from.
func (n *Node) ContainerDigest(uuid, repo string) (string, error) {
	servers, err := n.Servers()
	if err != nil {
		return "", err
	}
	for _, s := range servers {
		if s.UUID == uuid {
			return s.Digest(repo), nil
		}
	}
	return "", fmt.Errorf("no container for server %s on this node", uuid)
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

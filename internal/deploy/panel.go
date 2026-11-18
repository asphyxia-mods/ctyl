// Package deploy rolls verified images out to servers managed by a panel,
// using only documented client APIs, with graceful stops and rollback.
package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server is a game server as seen through the panel API.
type Server struct {
	ID    string // identifier used in API paths
	UUID  string // container name / hostname on the node
	Name  string
	Image string
}

// Panel is the subset of panel functionality a rollout needs.
type Panel interface {
	Name() string
	Servers() ([]Server, error)
	SetImage(id, image string) error
	Power(id, action string) error // start | stop | restart | kill
	State(id string) (string, error)
	// Backup creates a backup and waits for it to finish.
	Backup(id, name string, timeout time.Duration) error
}

var ErrUnsupported = errors.New("not supported by this panel")

type client struct {
	base  string
	token string
	http  *http.Client
}

func (c *client) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.base, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func newClient(base, token string) (*client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		return nil, fmt.Errorf("panel URL must be https (got %q)", base)
	}
	if token == "" {
		return nil, errors.New("panel API token is empty")
	}
	return &client{base: base, token: token, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

// Pterodactyl implements the Pterodactyl Panel client API
// (https://pterodactyl.io API reference, /api/client).
type Pterodactyl struct{ c *client }

func NewPterodactyl(base, token string) (*Pterodactyl, error) {
	c, err := newClient(base, token)
	return &Pterodactyl{c}, err
}

func (p *Pterodactyl) Name() string { return "pterodactyl" }

func (p *Pterodactyl) Servers() ([]Server, error) {
	var out []Server
	for page := 1; ; page++ {
		var resp struct {
			Data []struct {
				Attributes struct {
					Identifier  string `json:"identifier"`
					UUID        string `json:"uuid"`
					Name        string `json:"name"`
					DockerImage string `json:"docker_image"`
				} `json:"attributes"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					TotalPages int `json:"total_pages"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := p.c.do(http.MethodGet, fmt.Sprintf("/api/client?type=admin-all&page=%d", page), nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			a := d.Attributes
			out = append(out, Server{ID: a.Identifier, UUID: a.UUID, Name: a.Name, Image: a.DockerImage})
		}
		if page >= resp.Meta.Pagination.TotalPages {
			return out, nil
		}
	}
}

func (p *Pterodactyl) SetImage(id, image string) error {
	return p.c.do(http.MethodPut, "/api/client/servers/"+url.PathEscape(id)+"/settings/docker-image", map[string]string{"docker_image": image}, nil)
}

func (p *Pterodactyl) Power(id, action string) error {
	return p.c.do(http.MethodPost, "/api/client/servers/"+url.PathEscape(id)+"/power", map[string]string{"signal": action}, nil)
}

func (p *Pterodactyl) State(id string) (string, error) {
	var resp struct {
		Attributes struct {
			CurrentState string `json:"current_state"`
		} `json:"attributes"`
	}
	err := p.c.do(http.MethodGet, "/api/client/servers/"+url.PathEscape(id)+"/resources", nil, &resp)
	return resp.Attributes.CurrentState, err
}

func (p *Pterodactyl) Backup(id, name string, timeout time.Duration) error {
	var created struct {
		Attributes struct {
			UUID string `json:"uuid"`
		} `json:"attributes"`
	}
	if err := p.c.do(http.MethodPost, "/api/client/servers/"+url.PathEscape(id)+"/backups", map[string]any{"name": name}, &created); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var b struct {
			Attributes struct {
				IsSuccessful bool    `json:"is_successful"`
				CompletedAt  *string `json:"completed_at"`
			} `json:"attributes"`
		}
		if err := p.c.do(http.MethodGet, "/api/client/servers/"+url.PathEscape(id)+"/backups/"+url.PathEscape(created.Attributes.UUID), nil, &b); err != nil {
			return err
		}
		if b.Attributes.CompletedAt != nil {
			if !b.Attributes.IsSuccessful {
				return errors.New("backup failed")
			}
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("backup did not finish within %s", timeout)
}

// Calagopus implements the Calagopus Panel client API (/api/client), as
// defined in calagopus/panel backend/src/routes/api/client.
type Calagopus struct{ c *client }

func NewCalagopus(base, token string) (*Calagopus, error) {
	c, err := newClient(base, token)
	return &Calagopus{c}, err
}

func (p *Calagopus) Name() string { return "calagopus" }

func (p *Calagopus) Servers() ([]Server, error) {
	var out []Server
	for page := 1; ; page++ {
		var resp struct {
			Servers struct {
				Total   int `json:"total"`
				PerPage int `json:"per_page"`
				Data    []struct {
					UUID  string `json:"uuid"`
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"data"`
			} `json:"servers"`
		}
		if err := p.c.do(http.MethodGet, fmt.Sprintf("/api/client/servers?page=%d&per_page=100", page), nil, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Servers.Data {
			out = append(out, Server{ID: s.UUID, UUID: s.UUID, Name: s.Name, Image: s.Image})
		}
		if len(resp.Servers.Data) == 0 || page*resp.Servers.PerPage >= resp.Servers.Total {
			return out, nil
		}
	}
}

func (p *Calagopus) SetImage(id, image string) error {
	return p.c.do(http.MethodPut, "/api/client/servers/"+url.PathEscape(id)+"/startup/docker_image", map[string]string{"image": image}, nil)
}

func (p *Calagopus) Power(id, action string) error {
	return p.c.do(http.MethodPost, "/api/client/servers/"+url.PathEscape(id)+"/power", map[string]string{"action": action}, nil)
}

func (p *Calagopus) State(id string) (string, error) {
	var resp struct {
		Resources struct {
			State string `json:"state"`
		} `json:"resources"`
	}
	err := p.c.do(http.MethodGet, "/api/client/servers/"+url.PathEscape(id)+"/resources", nil, &resp)
	return resp.Resources.State, err
}

// Backup is not automated for Calagopus: its backup API returns an
// asynchronous job whose completion contract is not yet stable. Use the
// panel's backup schedules before maintenance windows instead.
func (p *Calagopus) Backup(string, string, time.Duration) error { return ErrUnsupported }

var pollInterval = 5 * time.Second

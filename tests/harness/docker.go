// Package harness drives the Docker Engine API the way Pterodactyl Wings and
// Calagopus Wings do (create, attach to a TTY stream, start, send console
// input, stop by command or signal), so tests exercise the same container
// contract as a real panel deployment.
package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const apiVersion = "v1.45"

type Docker struct {
	socket string
	http   *http.Client
}

func NewDocker() *Docker {
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		sock = strings.TrimPrefix(h, "unix://")
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}
	return &Docker{socket: sock, http: &http.Client{Transport: &http.Transport{DialContext: dial}}}
}

func (d *Docker) do(method, path string, query url.Values, body any, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ContainerSpec mirrors the subset of the Docker create body that Wings sets.
type ContainerSpec struct {
	Hostname     string              `json:"Hostname,omitempty"`
	AttachStdin  bool                `json:"AttachStdin"`
	AttachStdout bool                `json:"AttachStdout"`
	AttachStderr bool                `json:"AttachStderr"`
	OpenStdin    bool                `json:"OpenStdin"`
	Tty          bool                `json:"Tty"`
	Image        string              `json:"Image"`
	Env          []string            `json:"Env"`
	User         string              `json:"User,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   HostConfig          `json:"HostConfig"`
}

type Mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
}

type HostConfig struct {
	Mounts            []Mount                        `json:"Mounts,omitempty"`
	Tmpfs             map[string]string              `json:"Tmpfs,omitempty"`
	Memory            int64                          `json:"Memory,omitempty"`
	MemoryReservation int64                          `json:"MemoryReservation,omitempty"`
	MemorySwap        int64                          `json:"MemorySwap,omitempty"`
	PidsLimit         int64                          `json:"PidsLimit,omitempty"`
	SecurityOpt       []string                       `json:"SecurityOpt,omitempty"`
	ReadonlyRootfs    bool                           `json:"ReadonlyRootfs"`
	CapDrop           []string                       `json:"CapDrop,omitempty"`
	CapAdd            []string                       `json:"CapAdd,omitempty"`
	NetworkMode       string                         `json:"NetworkMode,omitempty"`
	PortBindings      map[string][]map[string]string `json:"PortBindings,omitempty"`
	UsernsMode        string                         `json:"UsernsMode,omitempty"`
}

type Container struct {
	d    *Docker
	ID   string
	conn net.Conn

	mu     sync.Mutex
	output bytes.Buffer
	notify chan struct{}
	closed chan struct{}
}

func (d *Docker) Create(name string, spec ContainerSpec) (*Container, error) {
	var resp struct{ ID string }
	if err := d.do(http.MethodPost, "/containers/create", url.Values{"name": {name}}, spec, &resp); err != nil {
		return nil, err
	}
	return &Container{d: d, ID: resp.ID, notify: make(chan struct{}, 1), closed: make(chan struct{})}, nil
}

// Attach opens the hijacked stdin/stdout stream like Wings' ContainerAttach.
// With Tty=true the stream is raw (no multiplexing headers).
func (c *Container) Attach() error {
	conn, err := net.Dial("unix", c.d.socket)
	if err != nil {
		return err
	}
	req := fmt.Sprintf("POST /%s/containers/%s/attach?stream=1&stdin=1&stdout=1&stderr=1 HTTP/1.1\r\nHost: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", apiVersion, c.ID)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		conn.Close()
		return fmt.Errorf("attach: %s", resp.Status)
	}
	c.conn = conn
	go func() {
		defer close(c.closed)
		buf := make([]byte, 32<<10)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.output.Write(buf[:n])
				c.mu.Unlock()
				select {
				case c.notify <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}

func (c *Container) Start() error {
	return c.d.do(http.MethodPost, "/containers/"+c.ID+"/start", nil, nil, nil)
}

// Send writes a console line, as the panel's console does.
func (c *Container) Send(line string) error {
	_, err := c.conn.Write([]byte(line + "\n"))
	return err
}

func (c *Container) Kill(signal string) error {
	return c.d.do(http.MethodPost, "/containers/"+c.ID+"/kill", url.Values{"signal": {signal}}, nil, nil)
}

func (c *Container) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

// WaitFor blocks until the console output contains needle.
func (c *Container) WaitFor(needle string, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		if strings.Contains(c.Output(), needle) {
			return nil
		}
		select {
		case <-c.notify:
		case <-c.closed:
			if strings.Contains(c.Output(), needle) {
				return nil
			}
			return fmt.Errorf("console closed before %q appeared", needle)
		case <-deadline:
			return fmt.Errorf("timed out after %s waiting for %q", timeout, needle)
		}
	}
}

// Wait blocks until the container exits and returns its exit code.
func (c *Container) Wait(timeout time.Duration) (int, error) {
	type result struct {
		code int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var resp struct {
			StatusCode int
		}
		err := c.d.do(http.MethodPost, "/containers/"+c.ID+"/wait", nil, nil, &resp)
		ch <- result{resp.StatusCode, err}
	}()
	select {
	case r := <-ch:
		<-c.closedOrTimeout(5 * time.Second)
		return r.code, r.err
	case <-time.After(timeout):
		return -1, fmt.Errorf("container did not exit within %s", timeout)
	}
}

func (c *Container) closedOrTimeout(d time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		select {
		case <-c.closed:
		case <-time.After(d):
		}
		close(ch)
	}()
	return ch
}

type Inspect struct {
	State struct {
		Pid       int
		OOMKilled bool
		ExitCode  int
		Running   bool
	}
	Config struct {
		User string
		Env  []string
	}
}

func (c *Container) Inspect() (*Inspect, error) {
	var i Inspect
	err := c.d.do(http.MethodGet, "/containers/"+c.ID+"/json", nil, nil, &i)
	return &i, err
}

// Top lists the processes running in the container.
func (c *Container) Top() ([][]string, error) {
	var resp struct{ Processes [][]string }
	err := c.d.do(http.MethodGet, "/containers/"+c.ID+"/top", url.Values{"ps_args": {"-o pid,user,args"}}, nil, &resp)
	return resp.Processes, err
}

func (c *Container) Remove() error {
	if c.conn != nil {
		c.conn.Close()
	}
	return c.d.do(http.MethodDelete, "/containers/"+c.ID, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
}

// Dial connects to the Docker socket; exported for helpers that need raw API access.
func Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		sock = strings.TrimPrefix(h, "unix://")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", sock)
}

// WaitForMatch blocks until the console output matches re.
func (c *Container) WaitForMatch(re *regexp.Regexp, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		if re.MatchString(c.Output()) {
			return nil
		}
		select {
		case <-c.notify:
		case <-c.closed:
			if re.MatchString(c.Output()) {
				return nil
			}
			return fmt.Errorf("console closed before %s appeared", re)
		case <-deadline:
			return fmt.Errorf("timed out after %s waiting for %s", timeout, re)
		}
	}
}

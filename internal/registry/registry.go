// Package registry reads published image metadata from an OCI registry with
// anonymous (or GITHUB_TOKEN) pull access.
package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// ErrNotFound means the tag does not exist yet.
var ErrNotFound = errors.New("manifest not found")

type Manifest struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations"`
}

const accept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json"

var client = &http.Client{Timeout: 60 * time.Second}

// Get fetches the manifest for repo ("ghcr.io/owner/name") at reference.
func Get(repo, reference string) (*Manifest, error) {
	host, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid repository %q", repo)
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.0.0.1:") {
		scheme = "http" // local test registries only
	}
	u := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, name, reference)
	resp, err := request(u, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err := fetchToken(challenge)
		if err != nil {
			return nil, err
		}
		if resp, err = request(u, token); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return nil, err
	}
	m.Digest = resp.Header.Get("Docker-Content-Digest")
	return &m, nil
}

func request(u, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

var paramRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

func fetchToken(challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	params := map[string]string{}
	for _, m := range paramRE.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" {
		return "", fmt.Errorf("invalid token realm %q", params["realm"])
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	realm.RawQuery = q.Encode()
	req, _ := http.NewRequest(http.MethodGet, realm.String(), nil)
	if user, tok := os.Getenv("REGISTRY_USERNAME"), os.Getenv("REGISTRY_TOKEN"); user != "" && tok != "" {
		req.SetBasicAuth(user, tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request: %s", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

func manifestURL(repo, reference string) (string, error) {
	host, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", fmt.Errorf("invalid repository %q", repo)
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.0.0.1:") {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, name, reference), nil
}

// do sends a request, answering one bearer-token challenge.
func do(method, u, contentType string, body []byte) (*http.Response, error) {
	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequest(method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	resp, err := send("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	token, err := fetchToken(challenge)
	if err != nil {
		return nil, err
	}
	return send(token)
}

// Retag points tag at the manifest stored under digest by re-uploading its
// exact bytes, so the tag resolves to the same digest (nothing is converted).
// Credentials come from REGISTRY_USERNAME / REGISTRY_TOKEN.
func Retag(repo, digest, tag string) error {
	getURL, err := manifestURL(repo, digest)
	if err != nil {
		return err
	}
	resp, err := do(http.MethodGet, getURL, "", nil)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", getURL, resp.Status)
	}
	if got := digestOf(raw); got != digest {
		return fmt.Errorf("registry returned content with digest %s for %s", got, digest)
	}
	mediaType := resp.Header.Get("Content-Type")
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.MediaType != "" {
		mediaType = probe.MediaType
	}
	putURL, err := manifestURL(repo, tag)
	if err != nil {
		return err
	}
	resp, err = do(http.MethodPut, putURL, mediaType, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("PUT %s: %s: %s", putURL, resp.Status, bytes.TrimSpace(msg))
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" && got != digest {
		return fmt.Errorf("registry stored %s under %s, expected %s", got, tag, digest)
	}
	return nil
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

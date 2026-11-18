// Package gitcache answers "does build X contain commit Y" questions using
// treeless bare clones (commit graph only, no file contents).
package gitcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var revRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ValidRev reports whether s is a plain hexadecimal commit id. Anything else
// (option-like strings, ref expressions) is rejected before reaching git.
func ValidRev(s string) bool { return revRE.MatchString(s) }

type Cache struct {
	Dir string
	// Offline skips fetching; repositories must already be cached.
	Offline bool

	mu      sync.Mutex
	fetched map[string]error
}

func New(dir string) *Cache { return &Cache{Dir: dir, fetched: map[string]error{}} }

func (c *Cache) repoDir(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.Dir, strings.TrimSuffix(filepath.Base(url), ".git")+"-"+hex.EncodeToString(sum[:6])+".git")
}

func (c *Cache) git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Ensure makes sure an up-to-date commit graph for url is cached. Each
// repository is fetched at most once per Cache.
func (c *Cache) Ensure(url string) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("refusing non-https repository %q", url)
	}
	dir := c.repoDir(url)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, done := c.fetched[url]; done {
		return dir, err
	}
	err := c.fetch(url, dir)
	c.fetched[url] = err
	return dir, err
}

func (c *Cache) fetch(url, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if c.Offline {
			return fmt.Errorf("repository %s not cached and offline mode is set", url)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if _, err := c.git(dir, "init", "--quiet", "--bare"); err != nil {
			return err
		}
	}
	if c.Offline {
		return nil
	}
	_, err := c.git(dir, "fetch", "--quiet", "--force", "--prune", "--filter=tree:0", "--no-auto-gc", "--", url,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	return err
}

// Resolve expands a (possibly abbreviated) commit id in url.
func (c *Cache) Resolve(url, rev string) (string, bool, error) {
	if !ValidRev(rev) {
		return "", false, fmt.Errorf("invalid commit id %q", rev)
	}
	dir, err := c.Ensure(url)
	if err != nil {
		return "", false, err
	}
	out, err := c.git(dir, "rev-parse", "--quiet", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(string(out)), true, nil
}

// IsAncestor reports whether ancestor is reachable from descendant (a commit
// is its own ancestor).
func (c *Cache) IsAncestor(url, ancestor, descendant string) (bool, error) {
	if !ValidRev(ancestor) || !ValidRev(descendant) {
		return false, fmt.Errorf("invalid commit ids %q %q", ancestor, descendant)
	}
	dir, err := c.Ensure(url)
	if err != nil {
		return false, err
	}
	_, err = c.git(dir, "merge-base", "--is-ancestor", ancestor, descendant)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// Tags returns tag name -> peeled commit for tags in url.
func (c *Cache) Tags(url string) (map[string]string, error) {
	dir, err := c.Ensure(url)
	if err != nil {
		return nil, err
	}
	out, err := c.git(dir, "for-each-ref", "--format=%(refname:strip=2) %(objectname) %(*objectname)", "refs/tags")
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		switch len(f) {
		case 2:
			tags[f[0]] = f[1]
		case 3:
			tags[f[0]] = f[2]
		}
	}
	return tags, nil
}

// Locate finds the first repository among urls that contains rev.
func (c *Cache) Locate(urls []string, rev string) (repo, full string, err error) {
	var errs []string
	for _, u := range urls {
		full, ok, err := c.Resolve(u, rev)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if ok {
			return u, full, nil
		}
	}
	if len(errs) > 0 {
		return "", "", errors.New(strings.Join(errs, "; "))
	}
	return "", "", nil
}

// TagDate returns the creation date (tagger date for annotated tags, commit
// date otherwise) of a tag, in RFC 3339.
func (c *Cache) TagDate(url, tag string) (string, error) {
	if strings.HasPrefix(tag, "-") || strings.ContainsAny(tag, " \t\n") {
		return "", fmt.Errorf("invalid tag %q", tag)
	}
	dir, err := c.Ensure(url)
	if err != nil {
		return "", err
	}
	out, err := c.git(dir, "for-each-ref", "--format=%(creatordate:iso-strict)", "refs/tags/"+tag)
	return strings.TrimSpace(string(out)), err
}

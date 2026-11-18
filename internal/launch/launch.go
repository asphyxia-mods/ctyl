// Package launch prepares the process environment and replaces the launcher
// with the server process via execve(2).
package launch

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"ctyl/internal/startup"
)

// Exit codes follow shell conventions so panels report familiar values.
const (
	ExitUsage       = 2
	ExitNotExec     = 126
	ExitNotFound    = 127
	ExitRootRefused = 3
)

// Config is everything Run needs from the outside world; main fills it from
// the real process and tests substitute fakes.
type Config struct {
	Args    []string // os.Args[1:]
	Environ []string
	Dir     string
	Stdout  io.Writer
	Stderr  io.Writer
	Euid    int
	// UIDMap is the content of /proc/self/uid_map.
	UIDMap string
	// InternalIP returns the container's primary address, or "".
	InternalIP func() string
	// Passwd returns the content of /etc/passwd (nil skips the check).
	Passwd func() []byte
	// Exec replaces the current process; it only returns on failure.
	Exec func(path string, argv, env []string) error
}

// Plan is the fully resolved process to execute.
type Plan struct {
	Path     string   `json:"path"`
	Argv     []string `json:"argv"`
	Env      []string `json:"env"`
	Warnings []string `json:"warnings,omitempty"`
	Source   string   `json:"source"` // "args" or "STARTUP"
}

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// Run executes the launcher and returns the process exit code. On success it
// never returns because Exec replaces the process image.
func Run(cfg Config) int {
	err := run(cfg)
	var ee *exitError
	if errors.As(err, &ee) {
		fmt.Fprintf(cfg.Stderr, "ctyl-launch: %s\n", ee.msg)
		return ee.code
	}
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "ctyl-launch: %v\n", err)
		return 1
	}
	return 0
}

func run(cfg Config) error {
	env := newEnvironment(cfg.Environ)
	args := cfg.Args
	printOnly := len(args) > 0 && args[0] == "--print-plan"
	if printOnly {
		args = args[1:]
	}

	if err := checkIdentity(cfg.Euid, cfg.UIDMap, env.get("CTYL_ALLOW_ROOT") == "1"); err != nil {
		return err
	}

	applyDefaults(env, cfg)

	plan, err := buildPlan(args, env, cfg.Dir)
	if err != nil {
		return err
	}

	if printOnly {
		enc := json.NewEncoder(cfg.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}

	if env.get("CTYL_QUIET") != "1" {
		printBanner(cfg.Stdout, env, plan)
	}
	for _, w := range plan.Warnings {
		fmt.Fprintf(cfg.Stderr, "ctyl-launch: warning: %s\n", w)
	}
	if cfg.Passwd != nil && !hasPasswdEntry(cfg.Passwd(), cfg.Euid) && env.get("CTYL_QUIET") != "1" {
		fmt.Fprintf(cfg.Stderr, "ctyl-launch: note: uid %d has no /etc/passwd entry; Java 17 and older report user.name and user.home as \"?\" "+
			"(run as 988:988, or enable the panel daemon's generated passwd mount)\n", cfg.Euid)
	}

	err = cfg.Exec(plan.Path, plan.Argv, plan.Env)
	code := ExitNotExec
	if errors.Is(err, syscall.ENOENT) {
		code = ExitNotFound
	}
	return &exitError{code, fmt.Sprintf("exec %s: %v", plan.Path, err)}
}

// checkIdentity refuses to run as real root. Root inside a user namespace
// (rootless Docker/Podman, userns-remap, Wings rootless mode) maps to an
// unprivileged host user and is allowed.
func checkIdentity(euid int, uidMap string, allowRoot bool) error {
	if euid != 0 || allowRoot || !isInitialUserNamespace(uidMap) {
		return nil
	}
	return &exitError{ExitRootRefused, "refusing to run as root in the initial user namespace; " +
		"run the container with a non-root --user (the image default is 988:988), " +
		"use a user namespace, or set CTYL_ALLOW_ROOT=1 to override"}
}

func isInitialUserNamespace(uidMap string) bool {
	fields := strings.Fields(uidMap)
	// An unreadable map is treated as the initial namespace: fail closed.
	return len(fields) == 0 || (len(fields) == 3 && fields[0] == "0" && fields[1] == "0" && fields[2] == "4294967295")
}

func applyDefaults(env *environment, cfg Config) {
	if env.get("TZ") == "" {
		env.set("TZ", "UTC")
	}
	if _, ok := env.lookup("INTERNAL_IP"); !ok && cfg.InternalIP != nil {
		if ip := cfg.InternalIP(); ip != "" {
			env.set("INTERNAL_IP", ip)
		}
	}
}

func buildPlan(args []string, env *environment, dir string) (*Plan, error) {
	plan := &Plan{}
	var argv []string
	switch {
	case len(args) > 0:
		plan.Source = "args"
		argv = args
	case env.get("STARTUP") != "":
		plan.Source = "STARTUP"
		cmd, err := startup.Parse(env.get("STARTUP"), startup.Options{
			Lookup:      env.lookup,
			Dir:         dir,
			Strict:      env.get("CTYL_STRICT_VARIABLES") == "1",
			ArgListVars: nameSet(env.get("CTYL_ARGLIST_VARIABLES")),
			AllowUnset:  env.get("CTYL_ALLOW_UNSET") == "1",
		})
		if err != nil {
			return nil, &exitError{ExitUsage, err.Error()}
		}
		for _, a := range cmd.Assignments {
			env.set(a.Name, a.Value)
		}
		argv = cmd.Argv
		plan.Warnings = cmd.Warnings
	default:
		return nil, &exitError{ExitUsage, "nothing to run: set the STARTUP environment variable or pass a command as arguments"}
	}

	path, err := lookPath(argv[0], env.get("PATH"), dir)
	if err != nil {
		return nil, err
	}
	plan.Path = path
	plan.Argv = argv
	plan.Env = env.list()
	return plan, nil
}

func nameSet(list string) map[string]bool {
	set := map[string]bool{}
	for _, n := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		set[n] = true
	}
	return set
}

// lookPath resolves a command like execvp(3) but ignores relative PATH
// entries, so a writable working directory can never shadow system binaries.
func lookPath(name, pathEnv, dir string) (string, error) {
	if strings.Contains(name, "/") {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		return path, checkExecutable(path, name)
	}
	if pathEnv == "" {
		pathEnv = "/usr/bin:/bin"
	}
	for _, d := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(d) {
			continue
		}
		candidate := filepath.Join(d, name)
		if checkExecutable(candidate, name) == nil {
			return candidate, nil
		}
	}
	return "", &exitError{ExitNotFound, fmt.Sprintf("%s: command not found in PATH %q (this image has no shell; only programs shipped in the image or in the server directory can be run)", name, pathEnv)}
}

func checkExecutable(path, name string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return &exitError{ExitNotFound, fmt.Sprintf("%s: %v", name, err)}
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return &exitError{ExitNotExec, fmt.Sprintf("%s: not an executable file", name)}
	}
	return nil
}

func printBanner(w io.Writer, env *environment, plan *Plan) {
	if id := env.get("CTYL_IMAGE"); id != "" {
		fmt.Fprintf(w, "[ctyl] image %s\n", id)
	}
	if javaHome := env.get("JAVA_HOME"); javaHome != "" {
		if rel, err := ReadRelease(filepath.Join(javaHome, "release")); err == nil {
			version := rel["JAVA_RUNTIME_VERSION"]
			if version == "" {
				version = rel["JAVA_VERSION"]
			}
			details := []string{rel["OS_NAME"] + "/" + rel["OS_ARCH"]}
			if impl := strings.TrimSpace(rel["IMPLEMENTOR"] + " " + rel["IMPLEMENTOR_VERSION"]); impl != "" {
				details = append([]string{impl}, details...)
			}
			fmt.Fprintf(w, "[ctyl] java %s (%s)\n", version, strings.Join(details, ", "))
		}
	}
	fmt.Fprintf(w, "[ctyl] exec %s\n", shellQuoteAll(plan.Argv))
}

// ReadRelease parses a JDK release file (KEY="value" lines).
func ReadRelease(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseRelease(f)
}

func ParseRelease(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out, sc.Err()
}

// shellQuoteAll renders argv so that copying it into a POSIX shell would
// reproduce the same arguments; it is for display only.
func shellQuoteAll(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@+,%", r) || r > 0x7f) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func hasPasswdEntry(passwd []byte, uid int) bool {
	want := strconv.Itoa(uid)
	for _, line := range strings.Split(string(passwd), "\n") {
		if f := strings.Split(line, ":"); len(f) > 2 && f[2] == want {
			return true
		}
	}
	return false
}

// PrimaryAddress returns the source address the kernel would use for outbound
// traffic. Connecting a UDP socket sends no packets.
func PrimaryAddress() string {
	for _, target := range []string{"192.0.2.1:9", "[2001:db8::1]:9"} {
		c, err := net.Dial("udp", target)
		if err != nil {
			continue
		}
		addr := c.LocalAddr().(*net.UDPAddr).IP.String()
		c.Close()
		return addr
	}
	return ""
}

type environment struct {
	order []string
	vals  map[string]string
}

func newEnvironment(environ []string) *environment {
	e := &environment{vals: map[string]string{}}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		e.set(k, v)
	}
	return e
}

func (e *environment) lookup(k string) (string, bool) { v, ok := e.vals[k]; return v, ok }
func (e *environment) get(k string) string            { return e.vals[k] }

func (e *environment) set(k, v string) {
	if _, ok := e.vals[k]; !ok {
		e.order = append(e.order, k)
	}
	e.vals[k] = v
}

func (e *environment) list() []string {
	keys := append([]string(nil), e.order...)
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+e.vals[k])
	}
	return out
}

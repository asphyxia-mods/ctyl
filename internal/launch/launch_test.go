package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const identityMap = "         0          0 4294967295\n"

func testConfig(t *testing.T, args []string, environ ...string) (Config, *bytes.Buffer, *bytes.Buffer, *[]string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var execd []string
	return Config{
		Args:       args,
		Environ:    append([]string{"PATH=/usr/bin:/bin"}, environ...),
		Dir:        t.TempDir(),
		Stdout:     &stdout,
		Stderr:     &stderr,
		Euid:       988,
		UIDMap:     identityMap,
		InternalIP: func() string { return "172.18.0.2" },
		Exec: func(path string, argv, env []string) error {
			execd = append([]string{path}, argv...)
			return errors.New("exec stubbed")
		},
	}, &stdout, &stderr, &execd
}

func TestRunParsesStartupAndExecs(t *testing.T) {
	cfg, stdout, _, execd := testConfig(t, nil, "STARTUP=true -Xmx{{SERVER_MEMORY}}M", "SERVER_MEMORY=1024")
	Run(cfg)
	if len(*execd) == 0 || filepath.Base((*execd)[0]) != "true" {
		t.Fatalf("exec not called with resolved path: %q", *execd)
	}
	if want := []string{"true", "-Xmx1024M"}; !reflect.DeepEqual((*execd)[1:], want) {
		t.Errorf("argv %q want %q", (*execd)[1:], want)
	}
	if !strings.Contains(stdout.String(), "[ctyl] exec true -Xmx1024M") {
		t.Errorf("banner missing: %q", stdout.String())
	}
}

func TestArgsTakePrecedenceOverStartup(t *testing.T) {
	cfg, _, _, execd := testConfig(t, []string{"true", "a b"}, "STARTUP=false")
	Run(cfg)
	if want := []string{"true", "a b"}; len(*execd) < 1 || !reflect.DeepEqual((*execd)[1:], want) {
		t.Errorf("argv %q want %q", *execd, want)
	}
}

func TestPrintPlan(t *testing.T) {
	cfg, stdout, _, execd := testConfig(t, []string{"--print-plan"}, "STARTUP=A=1 true \"x y\"", "TZ=")
	if code := Run(cfg); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*execd) != 0 {
		t.Fatal("--print-plan executed the command")
	}
	var plan Plan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Argv, []string{"true", "x y"}) || plan.Source != "STARTUP" {
		t.Errorf("plan %+v", plan)
	}
	for _, want := range []string{"A=1", "TZ=UTC", "INTERNAL_IP=172.18.0.2"} {
		if !contains(plan.Env, want) {
			t.Errorf("env missing %s: %q", want, plan.Env)
		}
	}
}

func TestInternalIPNotOverridden(t *testing.T) {
	cfg, stdout, _, _ := testConfig(t, []string{"--print-plan", "true"}, "INTERNAL_IP=10.0.0.5", "TZ=Europe/Berlin")
	Run(cfg)
	if !strings.Contains(stdout.String(), "INTERNAL_IP=10.0.0.5") || !strings.Contains(stdout.String(), "TZ=Europe/Berlin") {
		t.Errorf("existing values overridden: %s", stdout)
	}
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		environ []string
		code    int
		msg     string
	}{
		{"nothing to run", nil, nil, ExitUsage, "nothing to run"},
		{"syntax error", nil, []string{"STARTUP=java -jar x; rm -rf /"}, ExitUsage, `operator ";"`},
		{"missing variable", nil, []string{"STARTUP=java -jar {{NOPE}}"}, ExitUsage, "NOPE is not set"},
		{"command not found", []string{"definitely-not-a-command"}, nil, ExitNotFound, "command not found"},
		{"no shell available", nil, []string{"STARTUP=sh -c id", "PATH=/nonexistent"}, ExitNotFound, "no shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, stderr, execd := testConfig(t, tt.args, tt.environ...)
			if code := Run(cfg); code != tt.code {
				t.Errorf("exit %d want %d (%s)", code, tt.code, stderr)
			}
			if !strings.Contains(stderr.String(), tt.msg) {
				t.Errorf("stderr %q missing %q", stderr, tt.msg)
			}
			if len(*execd) != 0 {
				t.Error("exec called")
			}
		})
	}
}

func TestRelativePathEntriesIgnored(t *testing.T) {
	cfg, _, _, execd := testConfig(t, []string{"evil"}, "PATH=.:bin")
	if err := os.WriteFile(filepath.Join(cfg.Dir, "evil"), []byte("#!/x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := Run(cfg); code != ExitNotFound || len(*execd) != 0 {
		t.Errorf("relative PATH entry used: code %d exec %q", code, *execd)
	}
}

func TestExplicitRelativeCommand(t *testing.T) {
	cfg, _, _, execd := testConfig(t, []string{"./server"})
	if err := os.WriteFile(filepath.Join(cfg.Dir, "server"), []byte("#!/x"), 0o755); err != nil {
		t.Fatal(err)
	}
	Run(cfg)
	if len(*execd) == 0 || (*execd)[0] != filepath.Join(cfg.Dir, "server") {
		t.Errorf("exec %q", *execd)
	}
}

func TestNonExecutable(t *testing.T) {
	cfg, _, _, _ := testConfig(t, []string{"./data"})
	if err := os.WriteFile(filepath.Join(cfg.Dir, "data"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Run(cfg); code != ExitNotExec {
		t.Errorf("code %d", code)
	}
}

func TestRootPolicy(t *testing.T) {
	tests := []struct {
		name    string
		euid    int
		uidMap  string
		allow   bool
		refused bool
	}{
		{"non-root", 988, identityMap, false, false},
		{"real root refused", 0, identityMap, false, true},
		{"unreadable map fails closed", 0, "", false, true},
		{"real root explicitly allowed", 0, identityMap, true, false},
		{"rootless docker", 0, "0 1000 1\n1 100000 65536\n", false, false},
		{"userns remap", 0, "0 165536 65536\n", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkIdentity(tt.euid, tt.uidMap, tt.allow)
			if (err != nil) != tt.refused {
				t.Errorf("err = %v, refused want %v", err, tt.refused)
			}
		})
	}
}

func TestParseRelease(t *testing.T) {
	rel, err := ParseRelease(strings.NewReader("IMPLEMENTOR=\"wolfi\"\nJAVA_RUNTIME_VERSION=\"21.0.13.1+-wolfi-r0\"\nSOURCE=\".:git:7a7090bd432d+\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rel["IMPLEMENTOR"] != "wolfi" || rel["SOURCE"] != ".:git:7a7090bd432d+" {
		t.Errorf("%v", rel)
	}
}

func TestPasswdNote(t *testing.T) {
	cfg, _, stderr, _ := testConfig(t, []string{"true"})
	cfg.Passwd = func() []byte {
		return []byte("root:x:0:0::/root:/sbin/nologin\ncontainer:x:988:988::/home/container:/sbin/nologin\n")
	}
	cfg.Euid = 988
	Run(cfg)
	if strings.Contains(stderr.String(), "no /etc/passwd entry") {
		t.Errorf("note printed for a known uid: %s", stderr)
	}
	cfg.Euid = 4242
	Run(cfg)
	if !strings.Contains(stderr.String(), "uid 4242 has no /etc/passwd entry") {
		t.Errorf("note missing: %s", stderr)
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuoteAll([]string{"java", "-Dx=a b", "", "it's", "-Xmx1G", "wörld"})
	want := `java '-Dx=a b' '' 'it'\''s' -Xmx1G wörld`
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

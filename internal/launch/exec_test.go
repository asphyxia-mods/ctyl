package launch

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the program the launcher executes.
func TestMain(m *testing.M) {
	if mode := os.Getenv("CTYL_TEST_HELPER"); mode != "" {
		helper(mode)
		return
	}
	dir, err := os.MkdirTemp("", "ctyl-launch-test")
	if err != nil {
		panic(err)
	}
	binDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func helper(mode string) {
	switch {
	case mode == "status":
		status, _ := os.ReadFile("/proc/self/status")
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "SigBlk:") || strings.HasPrefix(line, "SigIgn:") {
				fmt.Println(strings.Join(strings.Fields(line), " "))
			}
		}
		fmt.Println("pid", os.Getpid())
		fmt.Println("args", strings.Join(os.Args[1:], "|"))
	case strings.HasPrefix(mode, "exit:"):
		code, _ := strconv.Atoi(strings.TrimPrefix(mode, "exit:"))
		os.Exit(code)
	case mode == "echo":
		io.Copy(os.Stdout, os.Stdin)
	case mode == "signal":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		fmt.Println("ready")
		s := <-ch
		fmt.Println("got", s)
		os.Exit(128 + int(s.(syscall.Signal)))
	}
}

var binDir, launcherBin string

func buildLauncher(t *testing.T) string {
	t.Helper()
	if launcherBin != "" {
		return launcherBin
	}
	out := filepath.Join(binDir, "ctyl-launch")
	cmd := exec.Command("go", "build", "-o", out, "ctyl/cmd/ctyl-launch")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, b)
	}
	launcherBin = out
	return out
}

func launcherCmd(t *testing.T, mode, startup string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(buildLauncher(t))
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"CTYL_TEST_HELPER=" + mode,
		"HELPER=" + self,
		"STARTUP=" + startup,
		"CTYL_QUIET=1",
	}
	return cmd
}

func TestExecReplacesProcess(t *testing.T) {
	cmd := launcherCmd(t, "status", `{{HELPER}} "a b" c`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, fmt.Sprintf("pid %d\n", cmd.Process.Pid)) {
		t.Errorf("target ran in a different process than the launcher (pid %d):\n%s", cmd.Process.Pid, text)
	}
	if !strings.Contains(text, "args a b|c\n") {
		t.Errorf("argv not preserved:\n%s", text)
	}
	if !strings.Contains(text, "SigBlk: 0000000000000000") {
		t.Errorf("launcher leaked a blocked signal mask into the target:\n%s", text)
	}
}

func TestExitCodePropagates(t *testing.T) {
	err := launcherCmd(t, "exit:42", "{{HELPER}}").Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 42 {
		t.Fatalf("exit = %v, want 42", err)
	}
}

func TestStdinPassesThrough(t *testing.T) {
	cmd := launcherCmd(t, "echo", "{{HELPER}}")
	cmd.Stdin = strings.NewReader("say hello\nstop\n")
	out, err := cmd.Output()
	if err != nil || string(out) != "say hello\nstop\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestSignalsReachTarget(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := launcherCmd(t, "signal", "{{HELPER}}")
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			r := bufio.NewReader(stdout)
			if line, _ := r.ReadString('\n'); line != "ready\n" {
				t.Fatalf("helper not ready: %q", line)
			}
			cmd.Process.Signal(sig)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 128+int(sig) {
					t.Errorf("exit %v", err)
				}
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				t.Fatal("target did not handle signal")
			}
		})
	}
}

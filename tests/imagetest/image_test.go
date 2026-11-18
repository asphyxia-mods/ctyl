// Package imagetest runs a built image under the same container settings as
// Pterodactyl Wings / Calagopus Wings and checks runtime behaviour.
//
//	CTYL_TEST_IMAGE=ctyl:java_21-amd64 CTYL_PROBE_JAR=build/probe/probe.jar go test ./tests/imagetest/
package imagetest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ctyl/tests/harness"
)

func image(t *testing.T) string {
	img := os.Getenv("CTYL_TEST_IMAGE")
	if img == "" {
		t.Skip("CTYL_TEST_IMAGE not set")
	}
	return img
}

func dataDir(t *testing.T) string {
	t.Helper()
	// Not t.TempDir: files the JVM creates as another UID (e.g. the font cache
	// in $HOME/.java) cannot be removed by the test user, which is expected.
	dir, err := os.MkdirTemp("", "ctyl-imagetest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	probe := os.Getenv("CTYL_PROBE_JAR")
	if probe == "" {
		t.Skip("CTYL_PROBE_JAR not set")
	}
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.jar"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

var seq int

func run(t *testing.T, s harness.Server, mutate func(*harness.ContainerSpec)) *harness.Container {
	t.Helper()
	spec := s.Spec()
	if mutate != nil {
		mutate(&spec)
	}
	seq++
	d := harness.NewDocker()
	c, err := d.Create(fmt.Sprintf("ctyl-it-%d-%d", os.Getpid(), seq), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("console output:\n%s", c.Output())
		}
		c.Remove()
	})
	if err := c.Attach(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return c
}

func expectExit(t *testing.T, c *harness.Container, want int, timeout time.Duration) {
	t.Helper()
	code, err := c.Wait(timeout)
	if err != nil {
		t.Fatal(err)
	}
	if code != want {
		t.Fatalf("exit code %d, want %d", code, want)
	}
}

func probeLines(out string) map[string]string {
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PROBE ") {
			f := strings.SplitN(line, " ", 4)
			if len(f) >= 3 {
				res[f[1]] = strings.Join(f[2:], " ")
			}
		}
	}
	return res
}

func checkProbe(t *testing.T, out string, required ...string) map[string]string {
	t.Helper()
	res := probeLines(out)
	for name, r := range res {
		if strings.HasPrefix(r, "FAIL") {
			t.Errorf("probe %s: %s", name, r)
		}
	}
	for _, name := range required {
		if _, ok := res[name]; !ok {
			t.Errorf("probe %s did not run", name)
		}
	}
	return res
}

func TestProbeUnderWingsProfiles(t *testing.T) {
	img := image(t)
	for _, profile := range []harness.Profile{harness.PterodactylWings, harness.CalagopusWings} {
		t.Run(string(profile), func(t *testing.T) {
			c := run(t, harness.Server{Image: img, DataDir: dataDir(t), Profile: profile, TZ: "Europe/Berlin",
				Startup: "java -Xms64M -XX:MaxRAMPercentage=75.0 -jar probe.jar check",
				Vars:    map[string]string{"PROBE_READONLY_ROOT": "1"}}, nil)
			expectExit(t, c, 0, 3*time.Minute)
			res := checkProbe(t, c.Output(), "identity", "capabilities", "pid1", "processes", "listeners", "readonly-root",
				"headless-awt-fonts", "crypto", "process-spawn", "plugin-native-libs", "unicode-filenames", "tls", "tls-rejects-invalid-certificates", "timezone")
			if want := os.Getenv("CTYL_EXPECT_RUNTIME"); want != "" && !strings.Contains(res["version"], want) {
				t.Errorf("runtime %q, expected %q", res["version"], want)
			}
			t.Logf("version: %s; modules: %s; identity: %s", res["version"], res["modules"], res["identity"])
		})
	}
}

func TestImageDefaultUserAndArbitraryUID(t *testing.T) {
	img := image(t)
	for _, user := range []string{"", "988:988", "4242:4242"} {
		t.Run("user="+user, func(t *testing.T) {
			c := run(t, harness.Server{Image: img, DataDir: dataDir(t), Startup: "java -jar probe.jar check",
				Vars: map[string]string{"PROBE_NETWORK": "0"}}, func(s *harness.ContainerSpec) { s.User = user })
			expectExit(t, c, 0, 3*time.Minute)
			res := checkProbe(t, c.Output(), "identity")
			want := map[string]string{"": "uid=988", "988:988": "uid=988", "4242:4242": "uid=4242"}[user]
			if !strings.Contains(res["identity"], want) {
				t.Errorf("identity %q, want %s", res["identity"], want)
			}
			// Java 19+ falls back to $HOME without a passwd entry; older
			// releases report "?" (documented in docs/compatibility.md).
			if (user != "4242:4242" || javaFeature(res["version"]) >= 19) && !strings.Contains(res["identity"], "user.home=/home/container") {
				t.Errorf("identity %q, want user.home=/home/container", res["identity"])
			}
			if user == "4242:4242" && !strings.Contains(c.Output(), "uid 4242 has no /etc/passwd entry") {
				t.Errorf("launcher did not explain the missing passwd entry")
			}
		})
	}
}

func javaFeature(version string) int {
	m := regexp.MustCompile(`ok (?:1\.)?(\d+)`).FindStringSubmatch(version)
	if m == nil {
		return 0
	}
	var n int
	fmt.Sscan(m[1], &n)
	return n
}

func TestMaximumHardening(t *testing.T) {
	img := image(t)
	c := run(t, harness.Server{Image: img, DataDir: dataDir(t), MemoryMB: 512, Network: "none",
		Startup: "java -XX:MaxRAMPercentage=50.0 -jar probe.jar check",
		Vars:    map[string]string{"PROBE_NETWORK": "0", "PROBE_READONLY_ROOT": "1"}}, func(s *harness.ContainerSpec) {
		s.HostConfig.CapDrop = []string{"ALL"}
		s.HostConfig.PidsLimit = 128
		s.HostConfig.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=64M"}
	})
	expectExit(t, c, 0, 3*time.Minute)
	checkProbe(t, c.Output(), "capabilities", "readonly-root", "headless-awt-fonts", "process-spawn")
}

func TestJavaAgent(t *testing.T) {
	c := run(t, harness.Server{Image: image(t), DataDir: dataDir(t), Startup: "java -javaagent:probe.jar -jar probe.jar agentcheck"}, nil)
	expectExit(t, c, 0, 2*time.Minute)
	checkProbe(t, c.Output(), "javaagent")
}

func TestContainerMemoryLimitRespected(t *testing.T) {
	c := run(t, harness.Server{Image: image(t), DataDir: dataDir(t), MemoryMB: 1024,
		Startup: "java -XX:MaxRAMPercentage=50.0 -XshowSettings:vm -version"}, nil)
	expectExit(t, c, 0, 2*time.Minute)
	m := regexp.MustCompile(`Max\. Heap Size \(Estimated\): ([0-9.]+)M`).FindStringSubmatch(c.Output())
	if m == nil {
		t.Fatalf("no heap size in output")
	}
	var mb float64
	fmt.Sscan(m[1], &mb)
	// Wings adds 15% overhead to the container limit: 1024M * 1.15 * 0.5 ≈ 589M.
	if mb < 450 || mb > 650 {
		t.Errorf("max heap %.0fM does not reflect the container memory limit", mb)
	}
}

func TestConsoleInputAndSignals(t *testing.T) {
	img := image(t)
	cases := []struct {
		name   string
		stop   func(c *harness.Container) error
		exit   int
		output string
	}{
		{"SIGTERM (docker stop / Wings SIGTERM)", func(c *harness.Container) error { return c.Kill("SIGTERM") }, 143, "shutdown hook ran"},
		{"SIGINT (Wings ^C stop)", func(c *harness.Container) error { return c.Kill("SIGINT") }, 130, "shutdown hook ran"},
		{"console exit command", func(c *harness.Container) error { return c.Send("exit 7") }, 7, "shutdown hook ran"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := run(t, harness.Server{Image: img, DataDir: dataDir(t), Startup: "java -jar probe.jar wait"}, nil)
			if err := c.WaitFor("ready", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := c.Send("say héllo wörld"); err != nil {
				t.Fatal(err)
			}
			if err := c.WaitFor("echo: say héllo wörld", 30*time.Second); err != nil {
				t.Fatal(err)
			}
			if err := tc.stop(c); err != nil {
				t.Fatal(err)
			}
			expectExit(t, c, tc.exit, time.Minute)
			if !strings.Contains(c.Output(), tc.output) {
				t.Errorf("missing %q", tc.output)
			}
		})
	}
}

func TestLauncherBanner(t *testing.T) {
	c := run(t, harness.Server{Image: image(t), DataDir: dataDir(t), Startup: "java -Xmx{{SERVER_MEMORY}}M -version", MemoryMB: 777}, nil)
	expectExit(t, c, 0, time.Minute)
	for _, want := range []string{"[ctyl] image ", "[ctyl] java ", "[ctyl] exec java -Xmx777M -version"} {
		if !strings.Contains(c.Output(), want) {
			t.Errorf("banner missing %q", want)
		}
	}
}

func TestShellSyntaxRejected(t *testing.T) {
	c := run(t, harness.Server{Image: image(t), DataDir: dataDir(t), Startup: "java -jar probe.jar check; touch /home/container/pwned"}, nil)
	expectExit(t, c, 2, time.Minute)
	if !strings.Contains(c.Output(), `shell operator ";" is not supported`) {
		t.Errorf("missing rejection message")
	}
}

func TestNoShellInImage(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/busybox", "/usr/bin/env"} {
		t.Run(shell, func(t *testing.T) {
			spec := harness.Server{Image: image(t), DataDir: t.TempDir(), Startup: "x"}.Spec()
			spec.Entrypoint = []string{shell}
			spec.Cmd = []string{"-c", "id"}
			d := harness.NewDocker()
			seq++
			c, err := d.Create(fmt.Sprintf("ctyl-it-%d-%d", os.Getpid(), seq), spec)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Remove()
			if err := c.Start(); err == nil {
				t.Fatalf("%s started", shell)
			}
		})
	}
}

func TestRootRefused(t *testing.T) {
	c := run(t, harness.Server{Image: image(t), DataDir: dataDir(t), Startup: "java -version"}, func(s *harness.ContainerSpec) { s.User = "0:0" })
	expectExit(t, c, 3, time.Minute)
}

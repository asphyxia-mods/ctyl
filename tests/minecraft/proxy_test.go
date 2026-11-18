package minecraft

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

const (
	velocityVersion = "4.3.1"
	velocityStartup = "java -Xms128M -Xmx{{SERVER_MEMORY}}M -XX:+UseG1GC -Dterminal.jline=false -Dterminal.ansi=true -jar {{SERVER_JARFILE}}"
	geyserReady     = "Run /geyser help for help!"
)

// Velocity 4.3 is compiled for Java 25 (class file 69).
var velocityGeyserLines = []int{25, 27}

// TestVelocityGeyser boots Velocity with Geyser, whose NetherNet transport
// loads a bundled WebRTC library (libdatachannel) that links against
// libssl.so.3, libcrypto.so.3 and libstdc++.so.6 from the image.
func TestVelocityGeyser(t *testing.T) {
	if os.Getenv("CTYL_MC_TESTS") != "1" {
		t.Skip("set CTYL_MC_TESTS=1 to run real Minecraft servers")
	}
	s := &suite{docker: harness.NewDocker(), pattern: envOr("CTYL_IMAGE_PATTERN", "ctyl:java_%d-amd64")}
	filter := regexp.MustCompile(envOr("CTYL_MC_SERVERS", "."))
	for _, line := range velocityGeyserLines {
		line := line
		name := fmt.Sprintf("java_%d/velocity-%s-geyser", line, velocityVersion)
		t.Run(name, func(t *testing.T) {
			if !filter.MatchString(name) {
				t.Skip("filtered")
			}
			if !s.imageExists(s.image(line)) {
				t.Skipf("image %s not present", s.image(line))
			}
			runVelocityGeyser(t, s, line)
		})
	}
}

func runVelocityGeyser(t *testing.T, s *suite, line int) {
	dir := t.TempDir()
	must(t, os.Chmod(dir, 0o755))
	proxy, err := velocityJar(velocityVersion)
	must(t, err)
	must(t, copyFile(proxy, filepath.Join(dir, "velocity.jar")))
	geyser, err := geyserJar("velocity")
	must(t, err)
	must(t, copyFile(geyser, filepath.Join(dir, "plugins", "Geyser-Velocity.jar")))

	srv := harness.Server{Image: s.image(line), DataDir: dir, Startup: velocityStartup, MemoryMB: 1024,
		Profile: harness.CalagopusWings, Vars: map[string]string{"SERVER_JARFILE": "velocity.jar"}}
	c := s.start(t, srv, nil)
	if err := c.WaitFor(geyserReady, 5*time.Minute); err != nil {
		t.Fatalf("Geyser not ready: %v\n%s", err, tail(c.Output(), 60))
	}

	out := c.Output()
	for _, failure := range []string{"NetherNet will not start", "UnsatisfiedLinkError"} {
		if strings.Contains(out, failure) {
			t.Errorf("%q in console:\n%s", failure, tail(out, 60))
		}
	}
	// Geyser creates its NetherNet data folder only after the native library
	// loaded, so this fails if NetherNet never reached that point.
	if folders, _ := filepath.Glob(filepath.Join(dir, "plugins", "*", "nethernet")); len(folders) == 0 {
		t.Errorf("no plugins/*/nethernet folder: NetherNet did not load its native library")
	}

	must(t, c.Send("end"))
	code, err := c.Wait(2 * time.Minute)
	if err != nil {
		t.Fatalf("end: %v\n%s", err, tail(c.Output(), 40))
	}
	if code != 0 {
		t.Errorf("end command exit code %d", code)
	}
}

package minecraft

// Real Minecraft server smoke tests.
//
//	CTYL_MC_TESTS=1 CTYL_MC_CACHE=$PWD/.cache/minecraft go test -timeout 3h ./tests/minecraft/
//
// Images default to ctyl:java_<N>-amd64 (override the pattern with
// CTYL_IMAGE_PATTERN); servers whose image is missing are skipped.
// CTYL_MC_SERVERS filters servers by regular expression. The tests accept the
// Minecraft EULA on the operator's behalf for these throwaway test servers.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ctyl/tests/harness"
)

const (
	paperStartup  = "java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true -jar {{SERVER_JARFILE}}"
	vectorStartup = "java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true --add-modules=jdk.incubator.vector -jar {{SERVER_JARFILE}}"
	legacyStartup = "java -Xms128M -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}}"
	globStartup   = "java -Xms128M -Xmx{{SERVER_MEMORY}}M -jar forge-*.jar"
	forgeStartup  = `java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true $( [[  ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" || printf %s "@unix_args.txt" )`
	neoStartup    = "java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true @unix_args.txt"
	eggDone       = ")! For help, type "
)

type serverCase struct {
	name    string
	line    int
	startup string
	memory  int64
	setup   func(t *testing.T, s *suite, dir string)
	// tickQuery is supported from 1.20.3 (vanilla command /tick query).
	tickQuery bool
}

func vanilla(version string) func(*testing.T, *suite, string) {
	return func(t *testing.T, _ *suite, dir string) {
		jar, err := vanillaJar(version)
		must(t, err)
		must(t, copyFile(jar, filepath.Join(dir, "server.jar")))
	}
}

func paper(version string) func(*testing.T, *suite, string) {
	return func(t *testing.T, _ *suite, dir string) {
		jar, err := paperJar(version)
		must(t, err)
		must(t, copyFile(jar, filepath.Join(dir, "server.jar")))
	}
}

func fabric(mc string, mods ...string) func(*testing.T, *suite, string) {
	return func(t *testing.T, _ *suite, dir string) {
		jar, err := fabricLauncherJar(mc)
		must(t, err)
		must(t, copyFile(jar, filepath.Join(dir, "server.jar")))
		for _, slug := range mods {
			mod, err := modrinthJar(slug, mc, "fabric")
			must(t, err)
			must(t, copyFile(mod, filepath.Join(dir, "mods", filepath.Base(mod))))
		}
	}
}

// installed runs a Forge/NeoForge installer once inside the line's own image
// (as an install container would) and caches the result.
func installed(kind, version string, line int, after func(t *testing.T, dir string)) func(*testing.T, *suite, string) {
	return func(t *testing.T, s *suite, dir string) {
		cached := filepath.Join(cacheDir(), "installed", kind+"-"+version)
		if _, err := os.Stat(filepath.Join(cached, ".ctyl-installed")); err != nil {
			base, group := "https://maven.minecraftforge.net", "net.minecraftforge"
			if kind == "neoforge" {
				base, group = "https://maven.neoforged.net/releases", "net.neoforged"
			}
			inst, err := mavenInstaller(base, group, kind, version)
			must(t, err)
			os.RemoveAll(cached)
			must(t, os.MkdirAll(cached, 0o755))
			must(t, copyFile(inst, filepath.Join(cached, "installer.jar")))
			c := s.start(t, harness.Server{Image: s.image(line), DataDir: cached, MemoryMB: 2048, Startup: "java -jar installer.jar --installServer"}, nil)
			code, err := c.Wait(20 * time.Minute)
			if err != nil || code != 0 {
				t.Fatalf("%s installer exit %d (%v):\n%s", kind, code, err, tail(c.Output(), 40))
			}
			os.Remove(filepath.Join(cached, "installer.jar"))
			os.Remove(filepath.Join(cached, "installer.jar.log"))
			if after != nil {
				after(t, cached)
			}
			must(t, os.WriteFile(filepath.Join(cached, ".ctyl-installed"), nil, 0o644))
		}
		must(t, copyTree(cached, dir))
		os.Remove(filepath.Join(dir, ".ctyl-installed"))
	}
}

// linkUnixArgs mirrors the eggs' `ln -sf libraries/.../*/unix_args.txt unix_args.txt`.
func linkUnixArgs(prefix string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		matches, _ := filepath.Glob(filepath.Join(dir, prefix, "*", "unix_args.txt"))
		if len(matches) != 1 {
			t.Fatalf("expected one unix_args.txt under %s, found %v", prefix, matches)
		}
		rel, _ := filepath.Rel(dir, matches[0])
		must(t, os.Symlink(rel, filepath.Join(dir, "unix_args.txt")))
	}
}

var servers = []serverCase{
	{name: "forge-1.12.2-glob-startup", line: 8, startup: globStartup, memory: 2048,
		setup: installed("forge", "1.12.2-14.23.5.2859", 8, nil)},
	{name: "vanilla-1.16.5", line: 8, startup: legacyStartup, memory: 1536, setup: vanilla("1.16.5")},
	{name: "vanilla-1.16.5", line: 11, startup: paperStartup, memory: 1536, setup: vanilla("1.16.5")},
	{name: "paper-1.20.4", line: 17, startup: paperStartup, memory: 1536, setup: paper("1.20.4"), tickQuery: true},
	{name: "forge-1.20.1-unix-args-idiom", line: 17, startup: forgeStartup, memory: 3072,
		setup: installed("forge", "1.20.1-47.4.28", 17, linkUnixArgs("libraries/net/minecraftforge/forge"))},
	{name: "paper-1.21.11-vector", line: 21, startup: vectorStartup, memory: 2048, setup: paper("1.21.11"), tickQuery: true},
	{name: "fabric-1.21.11-mods", line: 21, startup: legacyStartup, memory: 2048,
		setup: fabric("1.21.11", "fabric-api", "lithium", "spark"), tickQuery: true},
	{name: "neoforge-21.1.267", line: 21, startup: neoStartup, memory: 3072,
		setup: installed("neoforge", "21.1.267", 21, linkUnixArgs("libraries/net/neoforged/neoforge")), tickQuery: true},
	{name: "vanilla-26.3", line: 25, startup: paperStartup, memory: 2048, setup: vanilla("26.3"), tickQuery: true},
	{name: "paper-26.3", line: 25, startup: paperStartup, memory: 2048, setup: paper("26.3"), tickQuery: true},
	{name: "fabric-26.3-mods", line: 25, startup: legacyStartup, memory: 2048, setup: fabric("26.3", "fabric-api"), tickQuery: true},
	{name: "vanilla-26.3", line: 27, startup: paperStartup, memory: 2048, setup: vanilla("26.3"), tickQuery: true},
	{name: "paper-26.3", line: 27, startup: paperStartup, memory: 2048, setup: paper("26.3"), tickQuery: true},
}

type suite struct {
	docker  *harness.Docker
	pattern string
	seq     int
	mu      sync.Mutex
}

func (s *suite) image(line int) string { return fmt.Sprintf(s.pattern, line) }

func (s *suite) imageExists(name string) bool {
	tr := &http.Client{Transport: &http.Transport{DialContext: harnessDial}}
	resp, err := tr.Get("http://docker/v1.45/images/" + name + "/json")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func (s *suite) start(t *testing.T, srv harness.Server, mutate func(*harness.ContainerSpec)) *harness.Container {
	t.Helper()
	spec := srv.Spec()
	if mutate != nil {
		mutate(&spec)
	}
	s.mu.Lock()
	s.seq++
	name := fmt.Sprintf("ctyl-mc-%d-%d", os.Getpid(), s.seq)
	s.mu.Unlock()
	c, err := s.docker.Create(name, spec)
	must(t, err)
	t.Cleanup(func() {
		if dir := os.Getenv("CTYL_MC_LOGDIR"); dir != "" {
			os.MkdirAll(dir, 0o755)
			file := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "-" + name + ".log"
			os.WriteFile(filepath.Join(dir, file), []byte(c.Output()), 0o644)
		}
		c.Remove()
	})
	must(t, c.Attach())
	must(t, c.Start())
	return c
}

type result struct {
	Server          string  `json:"server"`
	Java            int     `json:"java"`
	Image           string  `json:"image"`
	FirstReadySec   float64 `json:"first_ready_seconds"`
	RestartReadySec float64 `json:"restart_ready_seconds"`
	RSSMB           int     `json:"rss_mb"`
	TickQuery       string  `json:"tick_query,omitempty"`
	Listeners       []int   `json:"listening_ports"`
	Processes       int     `json:"processes"`
	// SIGTERMSaveLogged reports whether the save was visible in the console;
	// SIGTERMPersisted whether world state actually survived the SIGTERM.
	SIGTERMSaveLogged bool   `json:"sigterm_save_logged"`
	SIGTERMPersisted  bool   `json:"sigterm_persisted"`
	JavaVersion       string `json:"java_version"`
}

func TestMinecraftServers(t *testing.T) {
	if os.Getenv("CTYL_MC_TESTS") != "1" {
		t.Skip("set CTYL_MC_TESTS=1 to run real Minecraft servers")
	}
	s := &suite{docker: harness.NewDocker(), pattern: envOr("CTYL_IMAGE_PATTERN", "ctyl:java_%d-amd64")}
	filter := regexp.MustCompile(envOr("CTYL_MC_SERVERS", "."))
	for _, sc := range servers {
		sc := sc
		name := fmt.Sprintf("java_%d/%s", sc.line, sc.name)
		t.Run(name, func(t *testing.T) {
			if !filter.MatchString(name) {
				t.Skip("filtered")
			}
			if !s.imageExists(s.image(sc.line)) {
				t.Skipf("image %s not present", s.image(sc.line))
			}
			runServer(t, s, sc)
		})
	}
}

func runServer(t *testing.T, s *suite, sc serverCase) {
	dir := t.TempDir()
	must(t, os.Chmod(dir, 0o755))
	sc.setup(t, s, dir)
	must(t, os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("eula=true\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "server.properties"), []byte(
		"server-port=25565\nview-distance=4\nsimulation-distance=4\nspawn-protection=0\nlevel-seed=ctyl\nenable-rcon=false\nenable-query=false\nmotd=ctyl smoke test\n"), 0o644))

	res := result{Server: sc.name, Java: sc.line, Image: s.image(sc.line)}
	srv := harness.Server{Image: s.image(sc.line), DataDir: dir, Startup: sc.startup, MemoryMB: sc.memory,
		Vars: map[string]string{"SERVER_JARFILE": "server.jar"}}

	// First boot under Pterodactyl Wings settings; stop with the egg's "stop" command.
	srv.Profile = harness.PterodactylWings
	start := time.Now()
	c := s.start(t, srv, nil)
	if err := c.WaitFor(eggDone, 15*time.Minute); err != nil {
		t.Fatalf("not ready: %v\n%s", err, tail(c.Output(), 60))
	}
	res.FirstReadySec = time.Since(start).Seconds()
	if m := regexp.MustCompile(`\[ctyl\] java (\S+)`).FindStringSubmatch(c.Output()); m != nil {
		res.JavaVersion = m[1]
	}

	// The TTY echoes typed input, so wait for the server's own output.
	must(t, c.Send("say ctyl-smoke-test"))
	if err := c.WaitFor("[Server] ctyl-smoke-test", time.Minute); err != nil {
		t.Errorf("console input not processed: %v", err)
	}
	must(t, c.Send("list"))
	if err := c.WaitFor("There are 0", time.Minute); err != nil {
		t.Errorf("list command: %v", err)
	}
	inspectRuntime(t, c, &res)
	if sc.tickQuery {
		time.Sleep(20 * time.Second)
		before := len(c.Output())
		must(t, c.Send("tick query"))
		if err := c.WaitFor("Average time per tick", 30*time.Second); err == nil {
			if m := regexp.MustCompile(`Average time per tick: ([0-9.]+ ?ms)`).FindStringSubmatch(c.Output()[before:]); m != nil {
				res.TickQuery = m[1]
			}
		}
	}
	must(t, c.Send("stop"))
	code, err := c.Wait(5 * time.Minute)
	if err != nil {
		t.Fatalf("stop: %v\n%s", err, tail(c.Output(), 40))
	}
	if code != 0 {
		t.Errorf("stop command exit code %d", code)
	}
	saved := regexp.MustCompile(`Saving (chunks|worlds)|ThreadedAnvilChunkStorage|All dimensions are saved`)
	if !saved.MatchString(c.Output()) {
		t.Errorf("no save on stop:\n%s", tail(c.Output(), 30))
	}
	level := filepath.Join(dir, "world", "level.dat")
	if _, err := os.Stat(level); err != nil {
		t.Fatalf("world not persisted: %v", err)
	}

	// Restart under Calagopus settings (fresh container, same data, as both
	// daemons recreate containers on every start); stop with SIGTERM.
	time.Sleep(1100 * time.Millisecond)
	srv.Profile = harness.CalagopusWings
	start = time.Now()
	c2 := s.start(t, srv, nil)
	if err := c2.WaitFor(eggDone, 10*time.Minute); err != nil {
		t.Fatalf("restart not ready: %v\n%s", err, tail(c2.Output(), 60))
	}
	res.RestartReadySec = time.Since(start).Seconds()
	// Change persistent world state, then stop with SIGTERM (what `docker
	// stop` and host shutdowns send). The saved world files prove whether the
	// JVM's shutdown hook saved, independent of what was logged; a third boot
	// proves the world still loads.
	must(t, c2.Send("scoreboard objectives add ctylsigterm dummy"))
	if err := c2.WaitForMatch(regexp.MustCompile(`(Created|Added) new objective[^\n]*ctylsigterm`), time.Minute); err != nil {
		t.Fatalf("scoreboard: %v", err)
	}
	must(t, c2.Kill("SIGTERM"))
	code, err = c2.Wait(5 * time.Minute)
	if err != nil {
		t.Fatalf("SIGTERM stop: %v", err)
	}
	if code != 0 && code != 143 {
		t.Errorf("SIGTERM exit code %d", code)
	}
	res.SIGTERMSaveLogged = saved.MatchString(c2.Output())
	res.SIGTERMPersisted = worldContains(t, filepath.Join(dir, "world"), "ctylsigterm")
	if !res.SIGTERMPersisted {
		t.Errorf("scoreboard objective created before SIGTERM is not in the saved world: SIGTERM did not save")
	}

	c3 := s.start(t, srv, nil)
	if err := c3.WaitFor(eggDone, 10*time.Minute); err != nil {
		t.Fatalf("third boot not ready: %v\n%s", err, tail(c3.Output(), 40))
	}
	must(t, c3.Send("stop"))
	if code, err := c3.Wait(5 * time.Minute); err != nil || code != 0 {
		t.Errorf("final stop: exit %d %v", code, err)
	}
	t.Logf("result: %+v", res)
	recordResult(t, res)
}

func inspectRuntime(t *testing.T, c *harness.Container, res *result) {
	procs, err := c.Top()
	if err == nil {
		res.Processes = len(procs)
		if len(procs) != 1 {
			t.Errorf("expected exactly one process in the container, got %v", procs)
		}
	}
	ins, err := c.Inspect()
	if err != nil || ins.State.Pid == 0 {
		return
	}
	if status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", ins.State.Pid)); err == nil {
		if m := regexp.MustCompile(`VmRSS:\s+(\d+) kB`).FindSubmatch(status); m != nil {
			kb, _ := strconv.Atoi(string(m[1]))
			res.RSSMB = kb / 1024
		}
	}
	for _, f := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", ins.State.Pid, f))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) > 3 && fields[3] == "0A" {
				port, _ := strconv.ParseInt(fields[1][strings.LastIndex(fields[1], ":")+1:], 16, 32)
				res.Listeners = append(res.Listeners, int(port))
			}
		}
	}
	for _, p := range res.Listeners {
		if p != 25565 {
			t.Errorf("unexpected listening port %d (only the game port is expected)", p)
		}
	}
}

// worldContains reports whether any (gzip-compressed NBT) .dat file under
// dir contains marker.
func worldContains(t *testing.T, dir, marker string) bool {
	found := false
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".dat") || found {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		var r io.Reader = f
		if gz, err := gzip.NewReader(f); err == nil {
			r = gz
		} else {
			f.Seek(0, 0)
		}
		data, _ := io.ReadAll(io.LimitReader(r, 64<<20))
		found = bytes.Contains(data, []byte(marker))
		return nil
	})
	return found
}

var resultsMu sync.Mutex

func recordResult(t *testing.T, r result) {
	path := os.Getenv("CTYL_MC_RESULTS")
	if path == "" {
		return
	}
	resultsMu.Lock()
	defer resultsMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Logf("results: %v", err)
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(r)
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

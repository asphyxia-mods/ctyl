// Package hygiene statically checks a built image for attack-surface
// regressions: shells, package managers, network tools, setuid files,
// unexpected writable paths and a dynamically linked launcher.
package hygiene

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"fmt"
	"path"
	"strings"

	"ctyl/internal/ociimage"
)

// ForbiddenExecutables must not exist anywhere in the image.
var ForbiddenExecutables = []string{
	"sh", "ash", "bash", "dash", "zsh", "ksh", "csh", "tcsh", "fish", "busybox", "toybox",
	"apk", "apt", "apt-get", "dpkg", "rpm", "yum", "dnf", "microdnf", "pip", "pip3",
	"curl", "wget", "git", "jq", "nc", "ncat", "netcat", "socat", "telnet", "ftp",
	"ssh", "sshd", "scp", "sftp", "dropbear",
	"gcc", "cc", "clang", "make", "ld", "as", "gdb", "strace", "ltrace",
	"python", "python3", "perl", "ruby", "node", "lua", "php",
	"su", "sudo", "doas", "login", "passwd", "crond", "cron", "syslogd", "rsyslogd",
	"systemctl", "init", "tini", "supervisord", "mount", "umount", "chroot",
}

// ForbiddenPackages must not be installed.
var ForbiddenPackages = []string{
	"busybox", "bash", "dash", "apk-tools", "curl", "wget", "git", "jq", "openssh", "openssh-client",
	"openssh-server", "dropbear", "python3", "perl", "build-base", "gcc", "make", "sudo", "shadow",
}

// writableAllowed are the only paths that may be writable by non-root users.
var writableAllowed = map[string]bool{"/tmp": true, "/var/tmp": true, "/home/container": true}

// RuntimeUID is the image's default (and Wings' default) container user.
const RuntimeUID = 988

type Options struct {
	Entrypoint         string
	DisableExecutables []string
}

type Violation struct {
	Path, Problem string
}

func (v Violation) String() string { return v.Path + ": " + v.Problem }

func Check(img *ociimage.Image, opts Options) []Violation {
	var out []Violation
	add := func(p, format string, args ...any) { out = append(out, Violation{p, fmt.Sprintf(format, args...)}) }

	forbidden := map[string]bool{}
	for _, n := range ForbiddenExecutables {
		forbidden[n] = true
	}
	for _, p := range img.Files() {
		h, _ := img.Header(p)
		isDevice := h.Typeflag == tar.TypeChar || h.Typeflag == tar.TypeBlock
		if forbidden[path.Base(p)] && executable(img, p) {
			add(p, "forbidden executable %q", path.Base(p))
		}
		if h.Mode&0o4000 != 0 || (h.Mode&0o2000 != 0 && h.Typeflag != tar.TypeDir) {
			add(p, "setuid/setgid bit set (mode %o)", h.Mode)
		}
		if isDevice || h.Typeflag == tar.TypeSymlink {
			continue
		}
		if h.Mode&0o002 != 0 && !writableAllowed[p] {
			add(p, "world-writable (mode %o)", h.Mode)
		}
		if writableAllowed[p] && p != "/home/container" && h.Mode&0o1000 == 0 {
			add(p, "world-writable directory without sticky bit (mode %o)", h.Mode)
		}
		if p != "/home/container" && (h.Uid == RuntimeUID || (h.Gid == RuntimeUID && h.Mode&0o020 != 0)) {
			add(p, "writable by the runtime user %d outside /home/container", RuntimeUID)
		}
	}

	if pkgs, err := img.Packages(); err != nil {
		add("/usr/lib/apk/db/installed", "cannot read installed packages: %v", err)
	} else {
		deny := map[string]bool{}
		for _, n := range ForbiddenPackages {
			deny[n] = true
		}
		for _, p := range pkgs {
			if deny[p.Name] {
				add("package "+p.Name, "forbidden package installed")
			}
		}
	}

	for _, exe := range opts.DisableExecutables {
		resolved, err := img.Resolve(exe)
		if err != nil {
			add(exe, "%v", err)
			continue
		}
		if h, ok := img.Header(resolved); ok && h.Mode&0o111 != 0 {
			add(exe, "should be disabled but is executable (mode %o)", h.Mode)
		}
	}

	cfg := img.Config.Config
	if len(cfg.Entrypoint) != 1 || cfg.Entrypoint[0] != opts.Entrypoint {
		add("config", "entrypoint is %q, want [%s]", cfg.Entrypoint, opts.Entrypoint)
	}
	if len(cfg.Cmd) != 0 {
		add("config", "unexpected default Cmd %q", cfg.Cmd)
	}
	if u := strings.Split(cfg.User, ":")[0]; u == "" || u == "0" || u == "root" {
		add("config", "image user %q is root", cfg.User)
	}
	if cfg.WorkingDir != "/home/container" {
		add("config", "working directory %q", cfg.WorkingDir)
	}

	if data, err := img.ReadFile(opts.Entrypoint); err != nil {
		add(opts.Entrypoint, "%v", err)
	} else if problem := staticProblem(data); problem != "" {
		add(opts.Entrypoint, "%s", problem)
	}
	return out
}

func executable(img *ociimage.Image, p string) bool {
	resolved, err := img.Resolve(p)
	if err != nil {
		return false
	}
	h, ok := img.Header(resolved)
	return ok && h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0
}

func staticProblem(data []byte) string {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "not an ELF executable: " + err.Error()
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "dynamically linked (has PT_INTERP)"
		}
	}
	if libs, _ := f.ImportedLibraries(); len(libs) > 0 {
		return fmt.Sprintf("links shared libraries %v", libs)
	}
	return ""
}

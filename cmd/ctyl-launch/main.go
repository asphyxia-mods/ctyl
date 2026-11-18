// Command ctyl-launch is the container entrypoint. It expands the panel's
// STARTUP template without a shell and execve(2)s the result, so the server
// process becomes PID 1 and receives signals, stdin and the TTY directly.
package main

import (
	"fmt"
	"os"
	"syscall"

	"ctyl/internal/launch"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("ctyl-launch", version)
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctyl-launch: working directory: %v\n", err)
		os.Exit(1)
	}
	uidMap, _ := os.ReadFile("/proc/self/uid_map")
	os.Exit(launch.Run(launch.Config{
		Args:       os.Args[1:],
		Environ:    os.Environ(),
		Dir:        dir,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Euid:       os.Geteuid(),
		UIDMap:     string(uidMap),
		InternalIP: launch.PrimaryAddress,
		Passwd: func() []byte {
			data, _ := os.ReadFile("/etc/passwd")
			return data
		},
		Exec: syscall.Exec,
	}))
}

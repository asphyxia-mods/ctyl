package harness

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// Profile selects which panel daemon's container settings to reproduce.
type Profile string

const (
	// PterodactylWings reproduces environment/docker/container.go in
	// pterodactyl/wings (Create).
	PterodactylWings Profile = "pterodactyl-wings"
	// CalagopusWings reproduces server/executor/docker in calagopus/wings,
	// which additionally drops CAP_SYS_PTRACE and hides the trash directory.
	CalagopusWings Profile = "calagopus-wings"
)

// Server describes a game server the way a panel does.
type Server struct {
	Image    string
	DataDir  string
	Startup  string            // raw egg startup template with {{VARIABLES}}
	Vars     map[string]string // egg variables (become upper-case env vars)
	MemoryMB int64
	Port     int
	User     string // "uid:gid"; Wings uses its system user
	Profile  Profile
	TZ       string
	// Network overrides the network mode (default "bridge").
	Network string
}

// wingsOverhead mirrors Wings' default docker.overhead multipliers.
func wingsOverhead(mb int64) float64 {
	switch {
	case mb <= 2048:
		return 1.15
	case mb <= 4096:
		return 1.10
	}
	return 1.05
}

func (s Server) Spec() ContainerSpec {
	if s.Port == 0 {
		s.Port = 25565
	}
	if s.MemoryMB == 0 {
		s.MemoryMB = 2048
	}
	if s.TZ == "" {
		s.TZ = "UTC"
	}
	if s.User == "" {
		s.User = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	if s.Profile == "" {
		s.Profile = PterodactylWings
	}
	env := map[string]string{
		"TZ":                        s.TZ,
		"STARTUP":                   s.Startup,
		"SERVER_MEMORY":             fmt.Sprint(s.MemoryMB),
		"SERVER_IP":                 "0.0.0.0",
		"SERVER_PORT":               fmt.Sprint(s.Port),
		"P_SERVER_LOCATION":         "test",
		"P_SERVER_UUID":             "00000000-0000-4000-8000-000000000000",
		"P_SERVER_ALLOCATION_LIMIT": "0",
	}
	for k, v := range s.Vars {
		env[strings.ToUpper(k)] = v
	}
	var envList []string
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	sort.Strings(envList)

	memory := int64(math.Round(float64(s.MemoryMB) * wingsOverhead(s.MemoryMB) * 1024 * 1024))
	port := fmt.Sprintf("%d/tcp", s.Port)
	capDrop := []string{"setpcap", "mknod", "audit_write", "net_raw", "dac_override", "fowner", "fsetid", "net_bind_service", "sys_chroot", "setfcap"}
	tmpfs := map[string]string{"/tmp": "rw,exec,nosuid,size=100M"}
	if s.Profile == CalagopusWings {
		capDrop = append(capDrop, "sys_ptrace")
		tmpfs["/home/container/.trash"] = "ro,nosuid,nodev,noexec,size=4k"
	}
	network := s.Network
	if network == "" {
		network = "bridge"
	}
	return ContainerSpec{
		Hostname:     "ctyl-test",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		Tty:          true,
		Image:        s.Image,
		Env:          envList,
		User:         s.User,
		Labels:       map[string]string{"Service": "Pterodactyl", "ContainerType": "server_process", "dev.ctyl.test": "1"},
		ExposedPorts: map[string]struct{}{port: {}},
		HostConfig: HostConfig{
			Mounts:            []Mount{{Type: "bind", Source: s.DataDir, Target: "/home/container"}},
			Tmpfs:             tmpfs,
			Memory:            memory,
			MemoryReservation: s.MemoryMB * 1024 * 1024,
			MemorySwap:        memory,
			PidsLimit:         512,
			SecurityOpt:       []string{"no-new-privileges"},
			ReadonlyRootfs:    true,
			CapDrop:           capDrop,
			NetworkMode:       network,
		},
	}
}

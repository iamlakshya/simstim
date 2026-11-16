//go:build linux

package gvisor

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

// These closed OCI records deliberately have no hooks, image, host network,
// user namespace or caller-supplied process/environment fields.
type ociSpec struct {
	Version string     `json:"ociVersion"`
	Process ociProcess `json:"process"`
	Root    ociRoot    `json:"root"`
	Mounts  []ociMount `json:"mounts"`
	Linux   ociLinux   `json:"linux"`
}
type ociProcess struct {
	Terminal        bool     `json:"terminal"`
	User            ociUser  `json:"user"`
	Args            []string `json:"args"`
	Env             []string `json:"env"`
	Cwd             string   `json:"cwd"`
	NoNewPrivileges bool     `json:"noNewPrivileges"`
}
type ociUser struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}
type ociRoot struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}
type ociLinux struct {
	Namespaces []ociNamespace `json:"namespaces"`
}
type ociNamespace struct {
	Type string `json:"type"`
}
type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options"`
}

func makeOCI(spec wall.Spec, plan []wall.Mount, smoke bool) (ociSpec, error) {
	if spec.Image != nil || spec.VM != nil || spec.Root == wall.Keep {
		return ociSpec{}, fmt.Errorf("gvisor needs a fresh process filesystem without an image")
	}
	args := []string{path.Join("/opt", brand.Name, "bin", brand.HostBinary), "wall-init"}
	if smoke {
		args = []string{"/usr/bin/true"}
	}
	result := ociSpec{Version: "1.2.1", Root: ociRoot{Path: "rootfs", Readonly: true},
		Process: ociProcess{User: ociUser{UID: 1000, GID: 1000}, Args: args, Env: []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"}, Cwd: "/work", NoNewPrivileges: true},
		Linux:   ociLinux{Namespaces: []ociNamespace{}},
		Mounts: []ociMount{
			{Destination: "/proc", Type: "proc", Source: "proc", Options: []string{"nosuid", "nodev", "noexec"}},
			{Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "mode=755"}},
			{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
			{Destination: "/dev/shm", Type: "tmpfs", Source: "shm", Options: []string{"nosuid", "nodev", "noexec", "mode=1777"}},
			{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777"}},
			{Destination: "/run", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=755", "uid=1000", "gid=1000"}},
			{Destination: "/home/agent", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=700", "uid=1000", "gid=1000"}},
		},
	}
	if smoke {
		result.Process.Cwd = "/"
	}
	seen := make(map[string]bool)
	for _, mount := range plan {
		if !filepath.IsAbs(mount.Host) || filepath.Clean(mount.Host) != mount.Host || !path.IsAbs(mount.Guest) || path.Clean(mount.Guest) != mount.Guest || mount.Guest == "/" || strings.ContainsAny(mount.Host+mount.Guest, "\x00\r\n") || seen[mount.Guest] {
			return ociSpec{}, fmt.Errorf("gvisor mount plan contains an unsafe or repeated path")
		}
		seen[mount.Guest] = true
		if mount.Guest == "/home" || mount.Guest == "/home/agent" {
			return ociSpec{}, fmt.Errorf("gvisor needs its own fresh agent home")
		}
		for _, private := range []string{"/proc", "/dev", "/tmp", "/run"} {
			if mount.Guest == private || strings.HasPrefix(mount.Guest, private+"/") {
				return ociSpec{}, fmt.Errorf("gvisor grant overlaps a private filesystem")
			}
		}
		mode := "ro"
		if mount.Write {
			mode = "rw"
		}
		result.Mounts = append(result.Mounts, ociMount{Destination: mount.Guest, Type: "bind", Source: mount.Host, Options: []string{"bind", mode, "nosuid", "nodev"}})
	}
	return result, nil
}

func runArguments(executable, session, id string, linkFD int) []string {
	return []string{executable, "--root", filepath.Join(session, "gvisor", "runsc"), "--platform=systrap", "--network=none", "--directfs=false", "--ignore-cgroups", "--host-uds=none", "--sidecar-release-enforcement-policy=ALWAYS", "run", "--bundle", filepath.Join(session, "gvisor", "bundle"), "--pass-fd", fmt.Sprintf("%d:3", linkFD), id}
}

// CommandArgumentsV1 returns the one production command of the original
// namespace owner. The authenticated receipt separately authorises its use.
func CommandArgumentsV1(session, id string) []string {
	return runArguments(path.Join(runtimeRoot(), "runsc"), session, id, 3)
}

// CommandEnvironmentV1 excludes ambient sidecar overrides and release skips.
func CommandEnvironmentV1() []string {
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
}

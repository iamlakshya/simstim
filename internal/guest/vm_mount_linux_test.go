//go:build linux

package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"golang.org/x/sys/unix"
)

type vmMountCall struct {
	source, target, kind string
	flags                uintptr
	data                 string
}

func vmMountFixture(t *testing.T) (*vmFiles, *[]vmMountCall) {
	t.Helper()
	base := t.TempDir()
	for _, name := range []string{"run", "mnt/root", "mnt/image", "mnt/agents", "mnt/session"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := []vmMountCall{}
	ops := vmMountOps{mount: func(s, d, k string, f uintptr, o string) error {
		calls = append(calls, vmMountCall{s, d, k, f, o})
		return nil
	}, unmount: func(string, int) error { return nil }, pivot: func(string, string) error { return nil }, chdir: func(string) error { return nil }, own: func(*os.File, int, int) error { return nil }, node: func(name string, _ uint32, _ int) error { return os.WriteFile(name, nil, 0o600) }, chmod: func(name string, mode uint32) error { return os.Chmod(name, os.FileMode(mode)) }}
	return newVMFiles(base, ops), &calls
}

func TestVMOverlayResetAndKeepUseSameFilesystemUpperWork(t *testing.T) {
	for _, system := range []string{"reset", "keep"} {
		for _, sudo := range []bool{false, true} {
			t.Run(system+map[bool]string{false: "-plain", true: "-sudo"}[sudo], func(t *testing.T) {
				fs, calls := vmMountFixture(t)
				if err := fs.overlay(system, sudo); err != nil {
					t.Fatal(err)
				}
				last := (*calls)[len(*calls)-1]
				if last.kind != "overlay" || last.flags&unix.MS_NODEV == 0 || (last.flags&unix.MS_NOSUID != 0) == sudo {
					t.Fatal("overlay privilege boundary lost", last)
				}
				parent := filepath.Join(fs.base, "run/overlay")
				if system == "keep" {
					parent = filepath.Join(fs.base, "mnt/session/system")
				}
				if !strings.Contains(last.data, "upperdir="+filepath.Join(parent, "upper")+",workdir="+filepath.Join(parent, "work")) {
					t.Fatal("upper/work are on different sources", last.data)
				}
				temporary := false
				for _, call := range *calls {
					if call.target == parent && call.kind == "tmpfs" {
						temporary = true
					}
				}
				if temporary != (system == "reset") {
					t.Fatal("system keep/reset changed persistence", *calls)
				}
			})
		}
	}
}

func TestVMLayoutKeepsWorkAndStateWhileHomeAndTmpAreFresh(t *testing.T) {
	fs, calls := vmMountFixture(t)
	plan := files.StatePlan{Dirs: []string{".claude/projects"}}
	if err := fs.layout(plan, false); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"home/agent": "tmpfs", "tmp": "tmpfs", "var/tmp": "tmpfs", "work": "", "home/agent/.claude/projects": "", "opt/agents": "", ".tools": ""}
	found := map[string]bool{}
	for _, call := range *calls {
		relative, _ := filepath.Rel(fs.root, call.target)
		kind, ok := expected[relative]
		if !ok || call.flags&unix.MS_REMOUNT != 0 {
			continue
		}
		if call.kind != kind {
			t.Fatal("wrong guest mount source", relative, call)
		}
		if kind == "tmpfs" && call.flags&(unix.MS_NOSUID|unix.MS_NODEV) != (unix.MS_NOSUID|unix.MS_NODEV) {
			t.Fatal("fresh user mount permits suid/devices", call)
		}
		if relative == "work" && call.source != filepath.Join(fs.base, "mnt/session/work") {
			t.Fatal("work is not on session disk")
		}
		if relative == "home/agent/.claude/projects" && call.source != filepath.Join(fs.base, "mnt/session/state/dirs/.claude/projects") {
			t.Fatal("kept history is not on session disk")
		}
		found[relative] = true
	}
	for name := range expected {
		if !found[name] {
			t.Fatal("required guest mount absent", name)
		}
	}
	for _, call := range *calls {
		if call.flags&unix.MS_REMOUNT != 0 && call.flags&unix.MS_NOSUID == 0 {
			t.Fatal("data/tools bind permits image suid", call)
		}
	}
}

func TestVMImmutableDiskMountsAndSessionRoles(t *testing.T) {
	fs, calls := vmMountFixture(t)
	if err := fs.mountDisks(); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatal("wrong disk mount count")
	}
	for i, call := range *calls {
		if (call.flags&unix.MS_RDONLY != 0) != (i < 2) || call.flags&unix.MS_NODEV == 0 || call.flags&unix.MS_NOSUID == 0 {
			t.Fatal("wrong disk write/device/suid role", call)
		}
	}
}

func TestVMFuseIsGuestOnlyAndRequiresSudoOrDocker(t *testing.T) {
	for _, allow := range []bool{false, true} {
		fs, _ := vmMountFixture(t)
		if err := fs.layout(files.StatePlan{}, allow); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(fs.root, "dev/fuse"))
		if (err == nil) != allow {
			t.Fatal("FUSE gate changed", allow, err)
		}
		for _, name := range []string{"dev/kvm", "dev/vda", "dev/vdb", "dev/vdd", "dev/net/tun"} {
			if _, err := os.Stat(filepath.Join(fs.root, name)); !os.IsNotExist(err) {
				t.Fatal("host/disk device exposed", name, err)
			}
		}
	}
}

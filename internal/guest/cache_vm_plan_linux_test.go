//go:build linux

package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"golang.org/x/sys/unix"
)

func TestVMKeptCachesUsePrivateSessionDiskPlanAndNoProgramMount(t *testing.T) {
	roots, err := sandbox.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for index := range 2 {
		fs, calls := vmMountFixture(t)
		if err := fs.layout(files.StatePlan{Dirs: roots}, false); err != nil {
			t.Fatal(err)
		}
		mounts := map[string]string{}
		for _, call := range *calls {
			if strings.HasPrefix(call.target, filepath.Join(fs.root, "home/agent")+string(filepath.Separator)) {
				mounts[call.target] = call.source
			}
		}
		for _, name := range roots {
			target := filepath.Join(fs.root, "home/agent", filepath.FromSlash(name))
			want := fs.boot("mnt/session/state/dirs/" + name)
			if mounts[target] != want {
				t.Fatal("cache source left private VM session disk", name, mounts[target], want)
			}
		}
		for _, call := range *calls {
			if call.flags&unix.MS_REMOUNT != 0 && strings.HasPrefix(call.target, filepath.Join(fs.root, "home/agent")+string(filepath.Separator)) && call.flags&(unix.MS_NODEV|unix.MS_NOSUID) != unix.MS_NODEV|unix.MS_NOSUID {
				t.Fatal("cache bind permits devices or image suid", call)
			}
		}
		if _, found := mounts[filepath.Join(fs.root, "home/agent/.cargo/bin")]; found {
			t.Fatal("program folder became retained mount")
		}
		entry := fs.boot("mnt/session/state/dirs/.npm/_cacache/entry")
		if index == 0 {
			first = entry
			if err := os.WriteFile(entry, []byte("private cache"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if _, err := os.Stat(entry); !os.IsNotExist(err) {
			t.Fatal("another VM saw cached bytes", err)
		}
	}
	if body, err := os.ReadFile(first); err != nil || string(body) != "private cache" {
		t.Fatal("second VM planning changed first cache", err)
	}
}

func TestVMCacheDropUsesFreshHomeAndLeavesOnlyItsPrivateDiskBytes(t *testing.T) {
	fs, calls := vmMountFixture(t)
	plan := files.StatePlan{Dirs: []string{".cache/pip", ".cache/uv"}}
	if err := fs.layout(plan, false); err != nil {
		t.Fatal(err)
	}
	cache := fs.boot("mnt/session/state/dirs/.cache/pip/content")
	if err := os.WriteFile(cache, []byte("private stored cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Reuse the same private session disk with a new root and fresh HOME.
	next := newVMFiles(fs.base, fs.ops)
	next.root = filepath.Join(fs.base, "mnt/next-root")
	if err := os.Mkdir(next.root, 0o700); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := next.layout(files.StatePlan{Dirs: []string{".cache/uv"}}, false); err != nil {
		t.Fatal(err)
	}
	freshHome, keptUV := false, false
	for _, call := range *calls {
		if call.target == filepath.Join(next.root, "home/agent") && call.kind == "tmpfs" {
			freshHome = true
		}
		if call.target == filepath.Join(next.root, "home/agent/.cache/pip") {
			t.Fatal("dropped cache was rebound from the old private disk")
		}
		if call.target == filepath.Join(next.root, "home/agent/.cache/uv") && call.source == next.boot("mnt/session/state/dirs/.cache/uv") {
			keptUV = true
		}
	}
	if !freshHome || !keptUV {
		t.Fatal("a path drop reset unrelated kept caches or reused HOME", freshHome, keptUV)
	}
	if body, err := os.ReadFile(cache); err != nil || string(body) != "private stored cache" {
		t.Fatal("dropping a bind removed the stored private cache", err)
	}
}

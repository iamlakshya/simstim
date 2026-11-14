//go:build linux

package process

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
)

func launcherPath(helperDir string) (string, error) {
	own := filepath.Join(helperDir, brand.UsernsBinary)
	if readInt(os.ReadFile, "/proc/sys/kernel/apparmor_restrict_unprivileged_userns") > 0 {
		installed := filepath.Join("/usr/local/libexec", brand.Name, brand.UsernsBinary)
		for p := installed; p != "/"; p = filepath.Dir(p) {
			st, err := os.Lstat(p)
			if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
				return "", setupError("the root-owned namespace launcher is absent or unsafe", err)
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 0 {
				return "", setupError("the namespace launcher is not owned by root", nil)
			}
		}
		own = installed
	}
	file, err := elf.Open(own)
	if err != nil {
		return "", setupError("the static namespace launcher is unavailable", err)
	}
	defer file.Close()
	for _, p := range file.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			return "", setupError("the namespace launcher must be static", nil)
		}
	}
	return own, nil
}

// LauncherCopyCurrent compares the root-owned and helper launchers without
// treating a different hash as executable capability evidence.
func LauncherCopyCurrent(helperDir string) bool {
	a, err := os.ReadFile(filepath.Join(helperDir, brand.UsernsBinary))
	if err != nil {
		return false
	}
	root, err := launcherPath(helperDir)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(root)
	return err == nil && sha256.Sum256(a) == sha256.Sum256(b)
}

func launcherSmoke(ctx context.Context) smokeResult {
	exe, err := os.Executable()
	if err != nil {
		return smokeResult{Error: "find helper executable"}
	}
	launcher, err := launcherPath(filepath.Dir(exe))
	if err != nil {
		return smokeResult{Error: "find trusted namespace launcher"}
	}
	dir, err := os.MkdirTemp("", "wall-probe-")
	if err != nil {
		return smokeResult{Error: "make probe root"}
	}
	defer os.RemoveAll(dir)
	root, err := pinSource(dir)
	if err != nil {
		return smokeResult{Error: "pin probe root"}
	}
	defer root.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return smokeResult{Error: "make probe request"}
	}
	defer reader.Close()
	defer writer.Close()
	// The small probe request fits a pipe, so no writer can outlive this call.
	if err = json.NewEncoder(writer).Encode(Request{V: RequestVersion, Probe: true, Mounts: []MountRequest{}}); err != nil {
		return smokeResult{Error: "write probe request"}
	}
	writer.Close()
	cmd := exec.CommandContext(ctx, launcher)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{reader, nil, root}
	b, err := cmd.Output()
	if err != nil {
		return smokeResult{Error: fmt.Sprintf("namespace launcher probe failed: %v", err)}
	}
	var result smokeResult
	if json.Unmarshal(b, &result) != nil {
		return smokeResult{Error: "read namespace launcher probe"}
	}
	return result
}

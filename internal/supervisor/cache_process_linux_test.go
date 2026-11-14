//go:build linux

package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// The real Prepare writes process.json; this test never calls Start, Probe,
// Stop, Kill or Destroy, and creates no process, namespace, service or mount.
func TestProcessCachePreparationPreservesLegacyPATHAndTypedFilePlans(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, t.TempDir())
	}
	programs, explicit := t.TempDir(), t.TempDir()
	for _, row := range []struct {
		name, id string
		files    *host.FilePlan
		headless bool
	}{
		{name: "legacy", id: "abc234de"},
		{name: "typed", id: "bcd345ef", files: &host.FilePlan{State: files.StatePlan{Files: []string{".agent/history"}}, Mounts: []wall.Mount{{Host: explicit, Guest: "/mnt/explicit"}}}},
		{name: "headless", id: "cde456fa", headless: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := os.MkdirAll(paths.SessionDir(row.id), 0o700); err != nil {
				t.Fatal(err)
			}
			m := &host.Manifest{ID: row.id, Files: row.files, Env: []string{"PATH=" + programs + ":/usr/bin:relative"}}
			box, err := prepareProcessMode(m, row.headless)
			if err != nil {
				t.Fatal(err)
			}
			if storage, ok := box.(ownedDiskRuntimeV1); ok {
				defer storage.ownedDiskResourcesV1().Close()
			}
			if state, ok := box.(interface{ StateCapture() *files.Capture }); ok && state.StateCapture() != nil {
				defer state.StateCapture().Close()
			}
			body, err := os.ReadFile(filepath.Join(paths.SessionDir(row.id), "process.json"))
			if err != nil {
				t.Fatal(err)
			}
			var spec wall.Spec
			if err := json.Unmarshal(body, &spec); err != nil {
				t.Fatal(err)
			}
			if m.Files != row.files || m.Files != nil && len(m.Files.State.Dirs) != 0 {
				t.Fatal("per-start cache defaults changed the protected FilePlan")
			}
			legacyPATH, explicitMount := false, false
			for _, mount := range spec.Mounts {
				legacyPATH = legacyPATH || mount.Host == programs && mount.Guest == programs && !mount.Write
				explicitMount = explicitMount || mount.Host == explicit && mount.Guest == "/mnt/explicit"
			}
			if legacyPATH != (row.files == nil) || explicitMount != (row.files != nil) {
				t.Fatal("synthetic cache FilePlan changed program-mount selection", spec.Mounts)
			}
			if row.headless {
				if len(spec.StateDirs) != 0 || len(spec.StateFiles) != 0 || spec.StateRestore != "" || spec.StateCapture != "" {
					t.Fatal("a stopped export-only boot gained retained cache state", spec)
				}
			} else if !slices.Contains(spec.StateDirs, ".npm/_cacache") || !slices.Contains(spec.StateDirs, ".local/share/pnpm/store") {
				t.Fatal("real Prepare did not consume effective kept-cache directories", spec.StateDirs)
			}
			if row.files != nil && (!slices.Equal(spec.StateFiles, row.files.State.Files) || spec.StateRestore == "" || spec.StateCapture == "") {
				t.Fatal("cache union changed copied-file generation roles", spec)
			}
		})
	}
}

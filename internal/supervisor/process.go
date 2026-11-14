package supervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// processSandbox adapts the public runtime to the supervisor's existing
// ownership contract. A failed Start cleans itself before returning an error.
type processSandbox struct {
	wall.Sandbox
	capture *files.Capture
	disk    *diskResourcesV1
}

func (s processSandbox) ownedDiskResourcesV1() *diskResourcesV1 { return s.disk }

func (s processSandbox) Start(ctx context.Context) (net.Conn, error) {
	return s.disk.start(ctx, s.Sandbox.Start)
}

// StateCapture is this run's private incoming area and read-only restore plan.
func (s processSandbox) StateCapture() *files.Capture { return s.capture }

func (s processSandbox) Wait() error { return s.Sandbox.Wait().Err }

// CleanExit reports the runtime's own completed exit and forced-kill evidence.
func (s processSandbox) CleanExit() bool {
	runtime, ok := s.Sandbox.(interface{ CleanExit() bool })
	return ok && runtime.CleanExit()
}

func prepareProcess(m *host.Manifest) (sandbox, error) { return prepareProcessMode(m, false) }

func prepareProcessMode(m *host.Manifest, headless bool) (sandbox, error) {
	helper, err := os.Executable()
	if err != nil {
		return nil, wallError("cannot find the supervisor helper", err)
	}
	dir, err := filepath.EvalSymlinks(paths.SessionDir(m.ID))
	if err != nil {
		return nil, wallError("cannot resolve this session's private folder", err)
	}
	workspace := filepath.Join(dir, "work")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		return nil, wallError("cannot make sandbox workspace", err)
	}
	mounts := []wall.Mount{{Host: workspace, Guest: "/work", Write: true}}
	plan, err := host.SandboxStatePlan(m)
	if headless {
		plan, err = files.StatePlan{}, nil
	}
	if err != nil {
		return nil, err
	}
	// Own a per-start plan, so removing keep at the next start is effective.
	hadFilePlan := m.Files != nil
	copy := *m
	if m.Files != nil {
		filePlan := *m.Files
		filePlan.State = plan
		copy.Files = &filePlan
	} else if len(plan.Files) > 0 || len(plan.Dirs) > 0 {
		copy.Files = &host.FilePlan{State: plan}
	}
	m = &copy
	capture, err := prepareFileCapture(m)
	if err != nil {
		return nil, err
	}
	prepared := false
	defer func() {
		if !prepared && capture != nil {
			_ = capture.Close()
		}
	}()
	if hadFilePlan {
		mounts = append(mounts, m.Files.Installs...)
		mounts = append(mounts, m.Files.Mounts...)
	} else {
		// Older manifests have no file plan. Only their already resolved PATH
		// folders enter, never the login environment.
		for _, folder := range filepath.SplitList(processPath(m.Env)) {
			if !filepath.IsAbs(folder) {
				continue
			}
			covered := false
			for _, prefix := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
				covered = covered || folder == prefix || strings.HasPrefix(folder, prefix+"/")
			}
			if !covered {
				mounts = append(mounts, wall.Mount{Host: folder, Guest: folder})
			}
		}
	}
	spec := wall.Spec{ID: m.ID, SessionDir: dir, Mounts: mounts, Helper: filepath.Dir(helper), Root: wall.Reset, MemoryMax: m.Limits.MemoryMax}
	if m.Files != nil {
		spec.StateDirs = append([]string(nil), m.Files.State.Dirs...)
		spec.StateFiles = append([]string(nil), m.Files.State.Files...)
	}
	if capture != nil {
		spec.StateRestore, spec.StateCapture = capture.Restore, capture.Incoming
	}
	if m.Policy != nil && m.Policy.Sandbox.DiskMax != nil {
		spec.DiskMax, err = api.ParseSize(*m.Policy.Sandbox.DiskMax)
		if err != nil || spec.DiskMax < 0 {
			return nil, diskFailure("disk_measure", "the process sandbox.disk_max cannot be used")
		}
	}

	sb, err := (process.Driver{}).Prepare(context.Background(), spec)
	if err != nil {
		return nil, err
	}
	disk, err := processDiskResourcesV1(spec)
	if err != nil {
		return nil, err
	}
	prepared = true
	return processSandbox{Sandbox: sb, capture: capture, disk: disk}, nil
}

func processPath(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			return value
		}
	}
	return ""
}

var _ sandbox = processSandbox{}

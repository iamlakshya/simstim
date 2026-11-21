package supervisor

import (
	"context"
	"errors"
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

// processSandbox adapts the process-filesystem runtimes to the supervisor's
// existing state capture and factory-owned disk resource contract.
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

func (s processSandbox) FreezeV1(ctx context.Context, maxMS int) error {
	owner, ok := s.Sandbox.(outsideFreezerV1)
	if !ok {
		return snapshotFailureV1("the original process runtime has no cgroup freezer", nil)
	}
	return owner.FreezeV1(ctx, maxMS)
}

func (s processSandbox) ThawV1() error {
	owner, ok := s.Sandbox.(outsideFreezerV1)
	if !ok {
		return snapshotFailureV1("the original process runtime has no cgroup thaw owner", nil)
	}
	return owner.ThawV1()
}

// CleanExit reports the runtime's own completed exit and forced-kill evidence.
func (s processSandbox) CleanExit() bool {
	runtime, ok := s.Sandbox.(interface{ CleanExit() bool })
	return ok && runtime.CleanExit()
}

func prepareProcess(m *host.Manifest) (sandbox, error) { return prepareProcessMode(m, false) }

func prepareProcessMode(m *host.Manifest, headless bool) (result sandbox, returned error) {
	spec, capture, err := prepareProcessFilesystem(m, headless)
	if err != nil {
		if capture != nil {
			return &failedSandboxPreparationV1{capture: capture}, err
		}
		return nil, err
	}
	prepared := false
	defer func() {
		if !prepared && capture != nil {
			closeErr := capture.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = &failedSandboxPreparationV1{capture: capture}
			}
		}
	}()
	sb, err := (process.Driver{}).Prepare(context.Background(), spec)
	if err != nil {
		if sb != nil {
			prepared = true // Transfer the SAME returned partial owner and capture.
			return &failedSandboxPreparationV1{runtime: sb, capture: capture}, err
		}
		return nil, err
	}
	disk, err := processDiskResourcesV1(spec)
	if err != nil {
		prepared = true
		return &failedSandboxPreparationV1{runtime: sb, capture: capture, disk: disk}, err
	}
	prepared = true
	return processSandbox{Sandbox: sb, capture: capture, disk: disk}, nil
}

// prepareProcessFilesystem is the ONE per-start process/gvisor file plan.
// It preserves the legacy PATH branch, canonical cache/state merge and owned
// capture generation. The caller owns a successful capture; failure closes it.
func prepareProcessFilesystem(m *host.Manifest, headless bool) (_ wall.Spec, capture *files.Capture, returned error) {
	if host.IsSandboxPrepareManifestV1(m) {
		headless = true
	}
	helper, err := os.Executable()
	if err != nil {
		return wall.Spec{}, nil, wallError("cannot find the supervisor helper", err)
	}
	dir, err := filepath.EvalSymlinks(paths.SessionDir(m.ID))
	if err != nil {
		return wall.Spec{}, nil, wallError("cannot resolve this session's private folder", err)
	}
	workspace := filepath.Join(dir, "work")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		return wall.Spec{}, nil, wallError("cannot make sandbox workspace", err)
	}
	mounts := []wall.Mount{{Host: workspace, Guest: "/work", Write: true}}
	plan, err := host.SandboxStatePlan(m)
	if headless {
		plan, err = files.StatePlan{}, nil
	}
	if err != nil {
		return wall.Spec{}, nil, err
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
	capture, err = prepareFileCapture(m)
	if err != nil {
		return wall.Spec{}, capture, err
	}
	defer func() {
		if returned != nil && capture != nil {
			closeErr := capture.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr == nil {
				capture = nil
			}
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
			return wall.Spec{}, capture, diskFailure("disk_measure", "the process sandbox.disk_max cannot be used")
		}
	}
	if m.Policy != nil && m.Policy.Sandbox.CPUs != nil && *m.Policy.Sandbox.CPUs > 0 {
		spec.CPUs = *m.Policy.Sandbox.CPUs
	}

	return spec, capture, nil
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

//go:build linux

package jail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/wall/process"
)

type deps struct {
	fs          jailFS
	userns      func(ctx context.Context, bwrapPath string) bool
	landlockABI func() int
	bwrapVer    func(ctx context.Context, bwrapPath string) string
	selfExe     func() (string, error)
}

func defaultDeps() deps {
	return deps{
		fs:          defaultJailFS,
		userns:      defaultUsernsForPath,
		landlockABI: landlockKernelABI,
		bwrapVer:    process.BwrapVersion,
		selfExe:     os.Executable,
	}
}

var (
	usernsOnce sync.Once
	usernsOK   bool
	usernsPath string
)

func defaultUserns() bool {
	usernsOnce.Do(func() {
		usernsPath, _ = exec.LookPath("bwrap")
		if usernsPath == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		usernsOK = probeUserNS(ctx, usernsPath)
	})
	return usernsOK
}

func defaultUsernsForPath(ctx context.Context, bwrapPath string) bool {
	ok := defaultUserns()
	if bwrapPath == "" || bwrapPath == usernsPath {
		return ok
	}
	return probeUserNS(ctx, bwrapPath)
}

// probeUserNS checks whether bubblewrap can create a user namespace and mount
// a tmpfs in it, without reading any sysctl.
func probeUserNS(ctx context.Context, bwrapPath string) bool {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return probeUserNSWith(tctx, bwrapPath, userNSProbeDeps{
		lookPath: exec.LookPath,
		runCmd: func(c context.Context, name string, args ...string) error {
			return exec.CommandContext(c, name, args...).Run()
		},
	})
}

func commandWithDeps(ctx context.Context, s Spec, d deps) (*exec.Cmd, error) {
	if err := validateSpec(s, d.fs); err != nil {
		return nil, err
	}

	bwrapPath := s.BwrapPath
	if bwrapPath == "" && d.fs.lookPath != nil {
		if p, err := d.fs.lookPath("bwrap"); err == nil {
			bwrapPath = p
		}
	}

	if bwrapPath != "" && d.userns != nil && d.userns(ctx, bwrapPath) {
		return commandBwrap(ctx, s, bwrapPath, d)
	}
	return commandNoUserNS(ctx, s, d)
}

func commandBwrap(ctx context.Context, s Spec, bwrapPath string, d deps) (*exec.Cmd, error) {
	bpfBytes, err := SeccompBPF(s.Seccomp, s.Net)
	if err != nil {
		return nil, err
	}
	secFile, err := memFile("jail-seccomp", bpfBytes)
	if err != nil {
		return nil, fmt.Errorf("jail: stage seccomp filter: %w", err)
	}

	secFD := 3 + len(s.Files)
	extraFiles := make([]*os.File, 0, len(s.Files)+2)
	extraFiles = append(extraFiles, s.Files...)
	extraFiles = append(extraFiles, secFile)

	disableUserNS := false
	if d.bwrapVer != nil {
		disableUserNS = bwrapSupportsDisableUserNS(d.bwrapVer(ctx, bwrapPath))
	}

	launchSpec := s
	if len(s.Rules) > 0 {
		helperExe, herr := resolveExecHelper(s.ExecHelper, d)
		if herr != nil {
			_ = secFile.Close()
			return nil, herr
		}
		targetPath := resolveTargetPath(s.Argv[0], d.fs)
		cfgFile, cerr := stageExecConfig(execConfig{
			TargetPath:  targetPath,
			Argv:        slices.Clone(s.Argv),
			Env:         buildEnvList(s.Env),
			Net:         s.Net,
			Binds:       slices.Clone(s.Binds),
			Rules:       slices.Clone(s.Rules),
			Profile:     s.Seccomp,
			InMountNS:   true,
			SkipSeccomp: true,
		})
		if cerr != nil {
			_ = secFile.Close()
			return nil, cerr
		}
		cfgFD := 3 + len(extraFiles)
		extraFiles = append(extraFiles, cfgFile)

		launchSpec = s
		// Keep the original target binary visible inside bwrap alongside the helper.
		if filepath.IsAbs(targetPath) && !isSystemPath(targetPath) {
			launchSpec.Binds = append(slices.Clone(s.Binds), Bind{Src: targetPath, Dest: targetPath, Mode: BindRO})
		}
		launchSpec.Argv = []string{helperExe, ExecSubcommand, "--config-fd", strconv.Itoa(cfgFD)}
	}

	args, err := buildBwrapArgsWithFS(launchSpec, secFD, disableUserNS, d.fs)
	if err != nil {
		closeOwnedExtras(extraFiles[len(s.Files):])
		return nil, err
	}

	cmd := exec.CommandContext(ctx, bwrapPath, args...)
	cmd.ExtraFiles = extraFiles
	return cmd, nil
}

func commandNoUserNS(ctx context.Context, s Spec, d deps) (*exec.Cmd, error) {
	abi := 0
	if d.landlockABI != nil {
		abi = d.landlockABI()
	}
	if err := checkNoUserNSLandlockABI(abi); err != nil {
		return nil, err
	}
	if err := checkNoUserNSBinds(s.Binds); err != nil {
		return nil, err
	}
	helperExe, err := resolveExecHelper(s.ExecHelper, d)
	if err != nil {
		return nil, err
	}
	targetPath := resolveTargetPath(s.Argv[0], d.fs)
	if !filepath.IsAbs(targetPath) {
		return nil, fmt.Errorf("jail: could not find executable %q on PATH. Try: pass an absolute path or install the program", s.Argv[0])
	}

	env := buildEnvList(s.Env)
	cfgFile, err := stageExecConfig(execConfig{
		TargetPath:  targetPath,
		Argv:        slices.Clone(s.Argv),
		Env:         env,
		Net:         s.Net,
		Binds:       slices.Clone(s.Binds),
		Rules:       slices.Clone(s.Rules),
		Profile:     s.Seccomp,
		InMountNS:   false,
		SkipSeccomp: false,
	})
	if err != nil {
		return nil, err
	}

	cfgFD := 3 + len(s.Files)
	extraFiles := make([]*os.File, 0, len(s.Files)+1)
	extraFiles = append(extraFiles, s.Files...)
	extraFiles = append(extraFiles, cfgFile)

	cmd := exec.CommandContext(ctx, helperExe, ExecSubcommand, "--config-fd", strconv.Itoa(cfgFD))
	cmd.Dir = s.Dir
	cmd.Env = env
	cmd.ExtraFiles = extraFiles
	return cmd, nil
}

func resolveExecHelper(override string, d deps) (string, error) {
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("jail: exec helper %q must be an absolute path. Try: pass an absolute helper path", override)
		}
		return filepath.Clean(override), nil
	}
	if d.selfExe == nil {
		return "", errors.New("jail: cannot resolve host helper executable. Try: set Spec.ExecHelper")
	}
	exe, err := d.selfExe()
	if err != nil {
		return "", fmt.Errorf("jail: locate host helper executable: %w. Try: set Spec.ExecHelper", err)
	}
	return filepath.Clean(exe), nil
}

func stageExecConfig(cfg execConfig) (*os.File, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("jail: encode exec config: %w", err)
	}
	f, err := memFile("jail-config", b)
	if err != nil {
		return nil, fmt.Errorf("jail: stage exec config: %w", err)
	}
	return f, nil
}

func closeOwnedExtras(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// memFile creates an anonymous readable file holding data, backed by memfd on
// Linux (falling back to an unlinked temp file if memfd_create is unavailable).
func memFile(name string, data []byte) (*os.File, error) {
	if fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC); err == nil {
		f := os.NewFile(uintptr(fd), name)
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("write memfd %s: %w", name, werr)
		}
		if _, serr := f.Seek(0, 0); serr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("seek memfd %s: %w", name, serr)
		}
		return f, nil
	}
	f, err := os.CreateTemp("", "jail-"+name+"-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file %s: %w", name, err)
	}
	_ = os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write temp file %s: %w", name, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek temp file %s: %w", name, err)
	}
	return f, nil
}

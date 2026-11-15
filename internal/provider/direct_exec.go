package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// resolveProviderEnvironment uses the actual explicit operation PATH only.
// It never reads ambient PATH/PATHEXT or tries a current-directory extension.
func resolveProviderEnvironment(name string, environment []string) (string, error) {
	if environment == nil {
		return "", fmt.Errorf("the explicit provider environment is unavailable")
	}
	if runtime.GOOS == "windows" {
		systemRoot := false
		for _, entry := range environment {
			key, value, ok := strings.Cut(entry, "=")
			if ok && strings.EqualFold(key, "SYSTEMROOT") && value != "" {
				systemRoot = true
			}
		}
		if !systemRoot {
			return "", fmt.Errorf("the explicit Windows provider environment has no system directory")
		}
	}
	if filepath.IsAbs(name) {
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(name), ".exe") {
			return "", fmt.Errorf("the provider metadata executable is not native")
		}
		info, err := os.Stat(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
			}
			return "", err
		}
		if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("the fixed provider executable is unavailable")
		}
		return name, nil
	}
	if filepath.Base(name) != name || name != "docker" && name != "limactl" {
		return "", fmt.Errorf("the explicit provider operation has no fixed executable")
	}
	path := ""
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(key, "PATH") {
			path = value
		}
	}
	leaf := name
	if runtime.GOOS == "windows" {
		leaf += ".exe"
	}
	for _, directory := range filepath.SplitList(path) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, leaf)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// runProviderEnvironment is the explicit-environment branch of the SAME
// provider executor. StartProcess selects its exact resolved native path on
// Windows too; os/exec's hidden extension search cannot change the selection.
// This is a direct read-only inspector/closed-create command, not a claim to
// contain arbitrary detached provider children or a second Git owner policy.
func runProviderEnvironment(ctx context.Context, name string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stdin != nil {
		return fmt.Errorf("the explicit provider role does not consume input")
	}
	name, err = resolveProviderEnvironment(name, environment)
	if err != nil {
		return err
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() {
		for _, file := range []*os.File{outRead, outWrite} {
			if closeErr := file.Close(); !errors.Is(closeErr, os.ErrClosed) {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() {
		for _, file := range []*os.File{errRead, errWrite} {
			if closeErr := file.Close(); !errors.Is(closeErr, os.ErrClosed) {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	process, err := os.StartProcess(name, append([]string{name}, args...), &os.ProcAttr{Env: slices.Clone(environment), Files: []*os.File{input, outWrite, errWrite}})
	if err != nil {
		return err
	}
	parentCloseErr := errors.Join(outWrite.Close(), errWrite.Close())
	streams := make(chan error, 2)
	go func() {
		_, err := io.Copy(stdout, outRead)
		if err != nil {
			_ = process.Kill()
		}
		streams <- errors.Join(err, outRead.Close())
	}()
	go func() {
		_, err := io.Copy(stderr, errRead)
		if err != nil {
			_ = process.Kill()
		}
		streams <- errors.Join(err, errRead.Close())
	}()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(stopped); _ = process.Kill(); _ = outRead.Close(); _ = errRead.Close() })
	state, waitErr := process.Wait()
	streamErr := errors.Join(<-streams, <-streams)
	if !stop() {
		<-stopped
	}
	if state == nil && waitErr == nil {
		waitErr = fmt.Errorf("the direct provider wait returned no state")
	}
	if state != nil && !state.Success() && waitErr == nil {
		waitErr = &exec.ExitError{ProcessState: state}
	}
	return errors.Join(ctx.Err(), parentCloseErr, waitErr, streamErr)
}

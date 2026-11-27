//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Every command has its original atomic creation descriptor, Wait and real
// pipe readers. N5 does not claim escaped filter descendants. An inherited
// writer which prevents EOF produces an error, never a clean inspection.
func runOriginalOrdinaryGitV2(parent context.Context, roots *ordinaryActiveGitRootsV2, args ...string) (_ string, returned error) {
	if parent == nil || roots == nil || roots.Work == nil || roots.Admin == nil || roots.Common == nil {
		return "", sessionLeaseError("the fixed Git command has no actual original directory loans")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	argv := slices.Concat(gitSafe, []string{"--git-dir=/proc/self/fd/4", "--work-tree=/proc/self/fd/3"}, args)
	command := exec.CommandContext(ctx, "git", argv...)
	command.Dir = "/"
	command.ExtraFiles = []*os.File{roots.Work, roots.Admin, roots.Common}
	command.Env = ordinaryGitEnvironmentV2()
	descriptor := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &descriptor}
	command.Cancel = func() error {
		if descriptor < 0 {
			return command.Process.Kill()
		}
		return unix.PidfdSendSignal(descriptor, unix.SIGKILL, nil, 0)
	}
	output, outputWrite, err := os.Pipe()
	if err != nil {
		return "", err
	}
	diagnostic, diagnosticWrite, err := os.Pipe()
	if err != nil {
		return "", errors.Join(err, output.Close(), outputWrite.Close())
	}
	defer func() { returned = errors.Join(returned, output.Close(), diagnostic.Close()) }()
	command.Stdout, command.Stderr = outputWrite, diagnosticWrite
	if err := command.Start(); err != nil {
		return "", errors.Join(err, outputWrite.Close(), diagnosticWrite.Close())
	}
	_ = outputWrite.Close()
	_ = diagnosticWrite.Close()
	if descriptor >= 0 {
		defer unix.Close(descriptor)
	}
	var stdout, stderr []byte
	var outputErr, diagnosticErr error
	var readers sync.WaitGroup
	readers.Add(2)
	read := func(file *os.File, raw *[]byte, failure *error) {
		defer readers.Done()
		*raw, *failure = io.ReadAll(io.LimitReader(file, 1<<20+1))
		if len(*raw) > 1<<20 {
			*failure = sessionLeaseError("the original fixed Git command exceeded its output bound")
			cancel()
		}
	}
	go read(output, &stdout, &outputErr)
	go read(diagnostic, &stderr, &diagnosticErr)
	// A context cancellation stops this original creation. Closing these real
	// readers then joins any outstanding read without waiting for an escaped
	// program's inherited pipe. Such a cancellation cannot supply counts.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = output.Close(); _ = diagnostic.Close() })
	waitErr := command.Wait()
	readers.Wait()
	if !stop() {
		<-done
	}
	if descriptor < 0 {
		return "", sessionLeaseError("the fixed Git command has no atomic original creation descriptor")
	}
	if err := errors.Join(ctx.Err(), outputErr, diagnosticErr); err != nil {
		return "", err
	}
	if waitErr != nil {
		return "", fmt.Errorf("the original fixed Git command failed: %w: %s", waitErr, firstLine(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

func ordinaryGitEnvironmentV2() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") || name == ordinaryGitSocketEnvV2 || name == gitScopeEnv {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_COMMON_DIR=/proc/self/fd/5", "GIT_OPTIONAL_LOCKS=0")
}

func checkOrdinaryGitVersionV2(ctx context.Context, roots *ordinaryActiveGitRootsV2) error {
	output, err := runOriginalOrdinaryGitV2(ctx, roots, "version")
	return checkGitVersionOutput([]byte(output), err)
}

func exactOrdinaryGitControlV2(root *os.Root, leaf string, expected []byte) error {
	control, err := captureOrdinaryGitControlFileV2(root, leaf)
	if err != nil {
		return err
	}
	defer control.file.Close()
	if !bytes.Equal(control.raw, expected) {
		return sessionLeaseError("the fixed Git worker observed a changed original administrative control")
	}
	return control.Check()
}

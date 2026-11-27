package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const gitSSHMode = "__git_ssh"

const gitSSHLeaseTimeout = 30 * time.Second

type gitSSHConfig struct {
	Version     int      `json:"version"`
	Name        string   `json:"name"`
	Args        []string `json:"args"`
	Target      string   `json:"target"`
	Root        string   `json:"root"`
	EntryDigest string   `json:"entry_digest"`
}

type gitSSHStatus struct {
	Exited     bool       `json:"exited"`
	Code       int        `json:"code"`
	Stopped    bool       `json:"stopped"`
	LocalError *UserError `json:"local_error,omitempty"`
}

// GitSSHMode runs the private Git SSH executor when args names it. The
// normal client and its test binaries dispatch it before command parsing.
// It owns SSH as an os.Process, so signals cannot target a reused PID.
func GitSSHMode(args []string) (int, bool) {
	if len(args) == 0 || args[0] != gitSSHMode {
		return 0, false
	}
	if len(args) < 3 {
		return 127, true
	}
	code := runGitSSH(args[1], args[2], args[3:])
	return code, true
}

func readGitSSHConfig(path, expectedDigest string) (gitSSHConfig, error) {
	var config gitSSHConfig
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return config, fmt.Errorf("open the private Git SSH directory: %w", err)
	}
	defer root.Close()
	base := filepath.Base(path)
	info, err := root.Lstat(base)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return config, errors.New("the private Git SSH configuration cannot be read")
	}
	f, err := root.Open(base)
	if err != nil {
		return config, fmt.Errorf("open the private Git SSH configuration: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return config, fmt.Errorf("read the private Git SSH configuration: %w", err)
	}
	sum := sha256.Sum256(b)
	if len(b) > 64<<10 || len(expectedDigest) != 64 || hex.EncodeToString(sum[:]) != expectedDigest {
		return config, errors.New("the private Git SSH configuration changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode the private Git SSH configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return config, errors.New("the private Git SSH configuration has trailing data")
	}
	if config.Version != 2 {
		return config, errors.New("the private Git SSH configuration has an unknown version")
	}
	if err := validateGitSSHConfig(config, filepath.Dir(path)); err != nil {
		return config, err
	}
	return config, nil
}

func runGitSSH(configPath, expectedDigest string, more []string) int {
	config, err := readGitSSHConfig(configPath, expectedDigest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	if err := validateGitSSHInvocation(more); err != nil || !gitSSHInvocationMatches(config, more) {
		return 127
	}
	dir := filepath.Dir(configPath)
	leaseRoot, err := os.OpenRoot(dir)
	if err != nil {
		return 127
	}
	defer leaseRoot.Close()
	if data, err := leaseRoot.ReadFile("lease"); err != nil || len(data) != 8 {
		return 128
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	leaseDone := make(chan struct{})
	defer close(leaseDone)
	go watchGitLease(stop, filepath.Join(dir, "lease"), leaseDone, gitSSHLeaseTimeout)
	command, release, err := prepareGitSSHStart(ctx, config, dir, more)
	if err != nil {
		writeGitLocalError(dir, err)
		fmt.Fprintln(os.Stderr, CleanLine(err.Error()))
		return 127
	}
	defer release()
	// Start still rejects a cancelled context. This function owns the
	// single graceful signal and bounded kill instead of exec's watcher.
	command.Cancel = nil
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		release()
		fmt.Fprintln(os.Stderr, "start SSH:", err)
		return 127
	}
	release()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	stopped := false
	select {
	case err = <-wait:
	case <-ctx.Done():
		stopped = true
		_ = terminateGitProcess(command.Process)
		select {
		case err = <-wait:
		case <-time.After(500 * time.Millisecond):
			_ = command.Process.Kill()
			err = <-wait
		}
	}
	status := gitSSHStatus{Code: -1, Stopped: stopped}
	if command.ProcessState != nil {
		status.Exited = command.ProcessState.Exited()
		status.Code = command.ProcessState.ExitCode()
	}
	data, encodeErr := json.Marshal(status)
	if encodeErr == nil {
		_ = writeAtomicFile(filepath.Join(dir, "status.json"), ".status-*", data)
	}
	if err == nil {
		return 0
	}
	if status.Code > 0 && status.Code <= 255 {
		return status.Code
	}
	return 128
}

// A changing counter uses monotonic observation time, so wall-clock changes
// cannot extend a dead client's lease. Missing/corrupt state stops SSH too.
func watchGitLease(stop context.CancelFunc, path string, done <-chan struct{}, timeout time.Duration) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		stop()
		return
	}
	defer root.Close()
	base := filepath.Base(path)
	last := time.Now()
	var previous uint64
	for {
		data, err := root.ReadFile(base)
		if err != nil || len(data) != 8 {
			stop()
			return
		}
		counter := binary.LittleEndian.Uint64(data)
		if counter != previous {
			previous = counter
			last = time.Now()
		}
		if time.Since(last) > timeout {
			stop()
			return
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

type gitLease struct {
	path     string
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
}

func startGitLease(dir string) (*gitLease, error) {
	path := filepath.Join(dir, "lease")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("make the private Git lease: %w", err)
	}
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], 1)
	if _, err := f.Write(data[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write the private Git lease: %w", err)
	}
	lease := &gitLease{path: path, done: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		defer close(lease.finished)
		defer f.Close()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		counter := uint64(1)
		for {
			select {
			case <-lease.done:
				return
			case <-ticker.C:
				counter++
				binary.LittleEndian.PutUint64(data[:], counter)
				if _, err := f.WriteAt(data[:], 0); err != nil {
					return
				}
			}
		}
	}()
	return lease, nil
}

func (lease *gitLease) close() {
	lease.once.Do(func() { close(lease.done); <-lease.finished; _ = os.Remove(lease.path) })
}

func readGitSSHStatus(path string) (gitSSHStatus, bool) {
	var status gitSSHStatus
	var record struct {
		Exited     *bool      `json:"exited"`
		Code       *int       `json:"code"`
		Stopped    *bool      `json:"stopped"`
		LocalError *UserError `json:"local_error"`
	}
	f, err := os.Open(path)
	if err != nil {
		return status, false
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1025))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return status, false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return status, false
	}
	if record.Exited == nil || record.Code == nil || record.Stopped == nil {
		return status, false
	}
	if record.LocalError != nil && (!validGitLocalError(record.LocalError) || *record.Exited || *record.Code != -1 || *record.Stopped) {
		return status, false
	}
	status = gitSSHStatus{Exited: *record.Exited, Code: *record.Code, Stopped: *record.Stopped, LocalError: record.LocalError}
	return status, status.Code >= -1 && status.Code <= 255
}

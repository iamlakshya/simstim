package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// What the debug bundle asks of ssh (decision 0025): the values that ssh
// would use for a host, and the log of one new connection with ssh -v.

// ConfigValues runs `ssh -G` for the host: the values that ssh would use to
// reach it, read from its config, with no connection. It returns each
// key, in lower case, with its values. When ssh's answer cannot be read to
// its end, it returns the values before that point, and an error.
func (s *SSH) ConfigValues(ctx context.Context) (map[string][]string, error) {
	cfg, err := writeWrapper(s.Host)
	if err != nil {
		return nil, err
	}
	var pin []string
	if ValidHostName(s.Host.Name) {
		var snap pinSnap
		var ok bool
		if lerr := readPinLocked(ctx, s.Host.Name, func() { snap, ok, err = loadPin(s.Host.Name) }); lerr != nil {
			return nil, lerr
		}
		if err != nil {
			return nil, err
		}
		if ok {
			if pin, err = pinArgs(s.Host.Name, snap.path); err != nil {
				return nil, err
			}
		}
	}
	// The forwarding options of every command line, and the pin, as the
	// bridge has them.
	vals, _, err := sshG(ctx, slices.Concat([]string{"-F", cfg}, NoForwarding(), pin, NoPortForwards()), s.Host.SSH)
	if err != nil {
		return vals, fmt.Errorf("ssh -G %s: %w", s.Host.Name, err)
	}
	return vals, nil
}

// maxConfigLine is the longest line of `ssh -G` that parseConfigValues
// reads. A longer one is an error, never a line that it leaves out.
const maxConfigLine = 1 << 20

// parseConfigValues reads what `ssh -G` prints: each key, in lower case,
// with its values. It reads every line or fails: a check that reads only
// part of the answer would miss the lines after the part that it read. On
// an error, it returns the values before the line that it could not read.
func parseConfigValues(out []byte) (map[string][]string, error) {
	vals := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), maxConfigLine)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok {
			k = strings.ToLower(k)
			vals[k] = append(vals[k], strings.TrimSpace(v))
		}
	}
	if err := sc.Err(); err != nil {
		return vals, fmt.Errorf("read the values that ssh -G printed: %w", err)
	}
	return vals, nil
}

// verboseWait is how long the bundle's ssh -v connection may take.
const verboseWait = 15 * time.Second

// VerboseLog opens one new connection to the host with ssh -v, and returns
// its log. It does not use or disturb the shared connection, never asks
// for a password, and stops after verboseWait. The remote side runs true.
func (s *SSH) VerboseLog(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := (&SSH{Host: s.Host}).connArgs(ctx, false)
	if err != nil {
		return nil, err // a saved host that has no pin yet, or an unsaved host
	}
	dir, err := os.MkdirTemp("", brand.Name+"-ssh-")
	if err != nil {
		return nil, fmt.Errorf("make a folder for the ssh log: %w", err)
	}
	defer os.RemoveAll(dir)
	log := filepath.Join(dir, "ssh.log")
	ctx, cancel := context.WithTimeout(ctx, verboseWait)
	defer cancel()
	// -v comes before the pin's LogLevel=ERROR, so ssh keeps DEBUG1: ssh
	// uses the first LogLevel or -v option on the command line.
	args := slices.Concat([]string{"-v"}, base, NoPortForwards(),
		[]string{"-T", "-e", "none", "-E", log, "--", s.Host.SSH, "true"})
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	runErr := cmd.Run()
	b, err := os.ReadFile(log)
	if err == nil && len(b) > 0 {
		return b, nil // a connection that failed still has a useful log
	}
	if runErr == nil {
		runErr = err
	}
	if runErr == nil {
		return nil, fmt.Errorf("ssh -v %s wrote no log", s.Host.Name)
	}
	return nil, fmt.Errorf("ssh -v %s wrote no log: %w", s.Host.Name, runErr)
}

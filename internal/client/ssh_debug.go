package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// What the debug bundle asks of ssh (decision 0025): the values that ssh
// would use for a host, and the log of one new connection with ssh -v.

// ConfigValues runs `ssh -G` for the host: the values that ssh would use to
// reach it, read from its config, with no connection. It returns each
// key, in lower case, with its values.
func (s *SSH) ConfigValues(ctx context.Context) (map[string][]string, error) {
	cfg, err := writeWrapper(s.Host)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-F", cfg, "-G", "--", s.Host.SSH).Output()
	if err != nil {
		return nil, fmt.Errorf("ssh -G %s: %w", s.Host.Name, err)
	}
	vals := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok {
			k = strings.ToLower(k)
			vals[k] = append(vals[k], strings.TrimSpace(v))
		}
	}
	return vals, nil
}

// verboseWait is how long the bundle's ssh -v connection may take.
const verboseWait = 15 * time.Second

// VerboseLog opens one new connection to the host with ssh -v, and returns
// its log. It does not use or disturb the shared connection, never asks
// for a password, and stops after verboseWait. The remote side runs true.
func (s *SSH) VerboseLog(ctx context.Context) ([]byte, error) {
	cfg, err := writeWrapper(s.Host)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", brand.Name+"-ssh-")
	if err != nil {
		return nil, fmt.Errorf("make a folder for the ssh log: %w", err)
	}
	defer os.RemoveAll(dir)
	log := filepath.Join(dir, "ssh.log")
	ctx, cancel := context.WithTimeout(ctx, verboseWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-F", cfg, "-T", "-o", "BatchMode=yes", "-o", "ControlMaster=no",
		"-o", "ControlPath=none", "-v", "-E", log, "--", s.Host.SSH, "true")
	cmd.WaitDelay = 2 * time.Second
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

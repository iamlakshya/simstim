package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

func gitSSHEntryDigest(h Host) (string, error) {
	pool, set := h.Pool()
	data, err := json.Marshal(struct {
		Host    Host `json:"host"`
		Pool    bool `json:"pool"`
		PoolSet bool `json:"pool_set"`
	}{entryOf(h), pool, set})
	if err != nil {
		return "", fmt.Errorf("encode the saved host for Git: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func prepareGitSSHStart(ctx context.Context, config gitSSHConfig, dir string, more []string) (*exec.Cmd, func(), error) {
	if err := validateGitSSHConfig(config, dir); err != nil {
		return nil, nil, err
	}
	ctx, unlock, err := LockHost(ctx, config.Name, "pull")
	if err != nil {
		return nil, nil, err
	}
	release := sync.OnceFunc(unlock)
	cmd, err := buildGitSSHStart(ctx, config, dir, more)
	if err != nil {
		release()
		return nil, nil, err
	}
	return cmd, release, nil
}

func buildGitSSHStart(ctx context.Context, config gitSSHConfig, dir string, more []string) (*exec.Cmd, error) {
	if err := validateGitSSHInvocation(more); err != nil || !gitSSHInvocationMatches(config, more) {
		return nil, badGitSSHConfig()
	}
	saved, err := SavedEntry(config.Name)
	if err != nil {
		return nil, err
	}
	if saved == nil || saved.SSH != config.Target {
		return nil, gitSSHEntryChanged(config.Name)
	}
	digest, err := gitSSHEntryDigest(*saved)
	if err != nil {
		return nil, err
	}
	if digest != config.EntryDigest {
		return nil, gitSSHEntryChanged(config.Name)
	}
	s := &SSH{Host: *saved}
	args, err := s.connArgs(ctx, true)
	if err != nil {
		return nil, err
	}
	args = append(args, NoPortForwards()...)
	args = append(args, "-E", filepath.Join(dir, "ssh.log"))
	// Git's validated prefix may add SendEnv=GIT_PROTOCOL. Query with that
	// exact prefix too, before adding the destination and remote command.
	prefix := more[:len(more)-2]
	if len(prefix) > 0 && prefix[len(prefix)-1] == "--" {
		prefix = prefix[:len(prefix)-1]
	}
	check := append(append([]string(nil), args...), prefix...)
	if err := s.checkSSHEnvironment(ctx, check); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ssh", append(args, more...)...) //nolint:gosec // G702: fixed executable, validated Git arguments and freshly pinned SSH options.
	return cmd, nil
}

func gitSSHEntryChanged(name string) *UserError {
	return &UserError{Msg: "the saved host changed before Git started SSH",
		Hint: brand.Name + " host show " + name + " (then run pull again)", Code: "host_changed"}
}

func validGitLocalError(e *UserError) bool {
	if e == nil || e.Msg == "" || e.Hint == "" || len(e.Msg) > 128 || len(e.Hint) > 192 || len(e.Data) != 0 {
		return false
	}
	if CleanLine(e.Msg) != e.Msg || CleanLine(e.Hint) != e.Hint {
		return false
	}
	switch e.Code {
	case "ssh_setenv":
		return e.Kind == KindRefused
	case "ssh_config_read", "host_changed", "host_busy":
		return e.Kind == KindFailed
	default:
		return false
	}
}

func writeGitLocalError(dir string, err error) {
	var ue *UserError
	if !errors.As(err, &ue) {
		return
	}
	e := *ue
	e.Data = nil
	e.Msg, e.Hint = CleanLine(e.Msg), CleanLine(e.Hint)
	if len(e.Msg) > 128 {
		e.Msg = strings.ToValidUTF8(e.Msg[:128], "")
	}
	if len(e.Hint) > 192 {
		e.Hint = "Check the host and SSH settings, then run pull again."
	}
	if !validGitLocalError(&e) {
		return
	}
	data, err := json.Marshal(gitSSHStatus{Code: -1, LocalError: &e})
	if err == nil {
		_ = writeAtomicFile(filepath.Join(dir, "status.json"), ".status-*", data)
	}
}

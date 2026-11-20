package onboard

import (
	"bytes"
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
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/e2b"
)

// Local is what the guided setup reads and changes on this computer. System
// is the real one; tests use fakes.
type Local interface {
	// Places lists where agents can run, and what each needs.
	Places() []Place
	// PublicKey is the user's SSH public key file, or "" when there is none.
	PublicKey() string
	// HasSSHHost reports whether ~/.ssh/config already names a host.
	HasSSHHost(name string) bool
	// AddSSHHost appends lines to ~/.ssh/config. Callers show the lines and
	// ask first.
	AddSSHHost(lines string) error
	// SSHWorks checks that `ssh target true` works without a prompt.
	SSHWorks(ctx context.Context, target string) error
	// Create makes a host with a provider and saves it (see Create).
	Create(ctx context.Context, provider, name string, out io.Writer) (client.Host, error)
	// SetIdle sets up idle stop on a created VM (see SetIdle).
	SetIdle(ctx context.Context, h client.Host) (string, error)
}

// System is the real computer.
type System struct {
	// Providers make hosts for the places Lima, Docker and E2B.
	Providers []provider.Provider
}

var _ Local = System{}

// Places probes this computer: ssh, limactl and docker on PATH, the SSH
// config's hosts, and an E2B key.
func (s System) Places() []Place {
	names := make([]string, 0, len(s.Providers))
	for _, p := range s.Providers {
		names = append(names, p.Name())
	}
	return Places(Probe{
		GOOS:      runtime.GOOS,
		Has:       func(n string) bool { _, err := exec.LookPath(n); return err == nil },
		SSHHosts:  client.SSHConfigHosts(),
		E2BKey:    e2b.KeyFound(),
		Providers: names,
	})
}

func sshDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".ssh")
}

// PublicKey looks for the usual key files in ~/.ssh, newest kind first.
func (System) PublicKey() string {
	dir := sshDir()
	if dir == "" {
		return ""
	}
	for _, n := range []string{"id_ed25519.pub", "id_ed25519_sk.pub", "id_ecdsa.pub", "id_ecdsa_sk.pub", "id_rsa.pub"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return "~/.ssh/" + n
		}
	}
	return ""
}

// HasSSHHost reads ~/.ssh/config and the files it includes.
func (System) HasSSHHost(name string) bool { return slices.Contains(client.SSHConfigHosts(), name) }

// AddSSHHost appends lines to ~/.ssh/config, after a blank line. It makes
// the folder and the file, private to the user, when they are missing.
func (System) AddSSHHost(lines string) error {
	dir := sshDir()
	if dir == "" {
		return errors.New("cannot find the home folder")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config")
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	lead := ""
	switch {
	case len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")):
		lead = "\n\n"
	case len(old) > 0:
		lead = "\n"
	}
	if _, err := f.WriteString(lead + lines); err != nil {
		f.Close()
		return fmt.Errorf("add to %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("add to %s: %w", path, err)
	}
	return nil
}

// SSHWorks runs `ssh -o BatchMode=yes target true`: it works only when no
// password, passphrase or host key question stands in the way. Like every
// ssh command of the client, it forwards nothing to the host. It reads the
// user's config through the client's generated one, which turns forwarding
// off for a ProxyJump hop too, and it has a connection of its own, so it
// never uses a master that the user's config names.
func (System) SSHWorks(ctx context.Context, target string) error {
	cfg, err := client.DefaultSSHConfig()
	if err != nil {
		var ue *client.UserError
		if errors.As(err, &ue) {
			return err
		}
		dir := filepath.Join(client.ConfigDir(), "ssh")
		return &client.UserError{Msg: "cannot write the SSH config that ssh needs: " + err.Error(),
			Hint: "Make " + dir + " a folder that you can write to, or set XDG_CONFIG_HOME to one."}
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := slices.Concat([]string{"-F", cfg, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"},
		client.NoForwarding(), client.NoPortForwards(), client.OwnConnection(), []string{"--", target, "true"})
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = 2 * time.Second // a child of ssh may hold stderr open
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := provider.Tail(errb.String(), 1)
		if msg == "" {
			msg = err.Error()
		}
		hint := "ssh " + target + "   (run it yourself to see what it asks)"
		if strings.Contains(msg, "Host key verification failed") {
			hint = "ssh " + target + "   (once, by hand: check the key's fingerprint, then answer yes)"
		}
		return &client.UserError{Msg: fmt.Sprintf("cannot log in to %s with ssh yet: %s", target, msg), Hint: hint}
	}
	return nil
}

// Create makes a host with the provider called kind (see Create). Progress
// lines go to out.
func (s System) Create(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
	for _, p := range s.Providers {
		if p.Name() == kind {
			return Create(ctx, p, provider.Request{Name: name, Out: out})
		}
	}
	return client.Host{}, &client.UserError{Msg: fmt.Sprintf("this build cannot make %s hosts", kind), Hint: "Pick another place."}
}

// SetIdle sets up idle stop (see SetIdle).
func (System) SetIdle(ctx context.Context, h client.Host) (string, error) { return SetIdle(ctx, h) }

package client

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// ForwardPair is one forwarded port.
type ForwardPair struct {
	// RemoteSocket is the exact checked private supervisor port socket.
	RemoteSocket string
	Local        int
	RemoteHost   string
	Remote       int
}

// Forward builds an ssh command that forwards local ports to the host until it
// is stopped. Each pair maps a local port to an address and port on the host.
//
// It has a connection of its own, so that its forwards, and those of the
// user's config, end when it ends, and never stay in the shared master that
// the bridge uses. It has ClearAllForwardings=no, so that a
// ClearAllForwardings yes in the user's config cannot clear its own -L
// options. And it refuses to start when the user's config would let the
// host reach this computer, with a RemoteForward or a Tunnel, or would send
// ssh to the background, with ForkAfterAuthentication (decision 0034).
func (s *SSH) Forward(ctx context.Context, pairs []ForwardPair) (*exec.Cmd, error) {
	return s.forwardCommand(ctx, pairs, nil)
}

func (s *SSH) forwardCommand(ctx context.Context, pairs []ForwardPair, extra []string) (*exec.Cmd, error) {
	args, err := s.connArgs(ctx, false)
	if err != nil {
		return nil, err
	}
	args = append(args, "-o", "ClearAllForwardings=no", "-N", "-o", "ExitOnForwardFailure=yes")
	for _, p := range pairs {
		if p.Local < 1 || p.Local > 65535 {
			return nil, &UserError{Msg: "invalid local forward port", Hint: "Choose a port from 1 to 65535.", Code: "bad_port", Kind: KindUsage}
		}
		if p.RemoteSocket != "" {
			if !path.IsAbs(p.RemoteSocket) || path.Clean(p.RemoteSocket) != p.RemoteSocket || CleanLine(p.RemoteSocket) != p.RemoteSocket || strings.ContainsAny(p.RemoteSocket, ":\x00\r\n%[]") {
				return nil, &UserError{Msg: "invalid supervisor port socket", Hint: "Grant the exact sandbox port again.", Code: "bad_port", Kind: KindRefused}
			}
			args = append(args, "-L", fmt.Sprintf("127.0.0.1:%d:%s", p.Local, p.RemoteSocket))
		} else {
			args = append(args, "-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", p.Local, p.RemoteHost, p.Remote))
		}
	}
	args = append(args, extra...)
	if err := s.checkForward(ctx, args); err != nil {
		return nil, err
	}
	args = append(args, "--", s.Host.SSH)
	return exec.CommandContext(ctx, "ssh", args...), nil
}

// checkForward reads `ssh -G` with Forward's options, which connects to
// nothing, and refuses a forward that would open a way from the host into
// this computer.
func (s *SSH) checkForward(ctx context.Context, opts []string) error {
	vals, stderr, err := sshG(ctx, opts, s.Host.SSH)
	if err != nil && vals == nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return &UserError{Msg: fmt.Sprintf("forward cannot check the SSH config for %s: %s", s.Host.Name, msg),
			Hint: "ssh -G " + ShellWord(s.Host.SSH) + " (check that this works first)", Code: "ssh_failed"}
	}
	if err != nil {
		// A line that is too long to read hides every line after it.
		return &UserError{Msg: fmt.Sprintf("forward cannot read all of what ssh -G prints for %s, so it cannot check it: %v", s.Host.Name, err),
			Hint: "Look for a very long line in your SSH config for " + s.Host.SSH + " (ssh -G " + ShellWord(s.Host.SSH) + " prints it).", Code: "ssh_config_read"}
	}
	return forwardRisk(s.Host, vals)
}

// forwardRisk refuses the config lines, as `ssh -G` prints them, that would
// let the host reach this computer while forward runs, or that would keep
// the forward running in the background after forward ends.
func forwardRisk(h Host, vals map[string][]string) error {
	var lines []string
	for _, v := range vals["remoteforward"] {
		lines = append(lines, "RemoteForward "+v)
	}
	if t := vals["tunnel"]; len(t) > 0 && t[0] != "false" {
		lines = append(lines, "Tunnel "+t[0])
	}
	// In the background, the forwards would outlive forward. OpenSSH before
	// 8.7 rejects the option, so it cannot go on the command line, and such
	// an ssh prints no such line either.
	if f := vals["forkafterauthentication"]; len(f) > 0 && f[0] != "no" {
		lines = append(lines, "ForkAfterAuthentication "+f[0])
	}
	if len(lines) == 0 {
		return nil
	}
	where := "~/.ssh/config"
	if h.SSHConfig != "" {
		where = h.SSHConfig + " or " + where
	}
	what := "that line"
	if len(lines) > 1 {
		what = "those lines"
	}
	return &UserError{
		Msg: fmt.Sprintf("your SSH config would let %s reach this computer, or keep the forward running after forward ends, so forward will not start: %s",
			h.Name, strings.Join(lines, ", ")),
		Hint: fmt.Sprintf("Remove %s from the block for %s in %s, then run %s forward again.", what, h.SSH, where, brand.Name),
		Code: "forward_risk", Kind: KindRefused,
	}
}

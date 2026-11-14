package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func portsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ports <host>/<session>",
		Short:   "List the TCP ports a session's processes listen on",
		Example: "  " + brand.Name + " ports my-vps/auth-fix\n  " + brand.Name + " ports my-vps/auth-fix --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				ports, err := conn.Ports(ctx, s.ID)
				if err != nil {
					return err
				}
				if asJSON {
					if ports == nil {
						ports = []api.Port{}
					}
					return printJSON(map[string]any{"host": h.Name, "session": s.Name, "id": s.ID, "ports": ports})
				}
				if len(ports) == 0 {
					say(cmd, "%s on %s listens on no TCP ports.\n", shown(nameOf(*s)), shown(h.Name))
					return nil
				}
				for _, p := range ports {
					printf("%-6d %-16s %s (pid %d)\n", p.Port, shown(p.Address), shown(p.Process), p.PID)
				}
				return nil
			})
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func forwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forward <host>/<session> [<port>…]",
		Short: "Forward a session's ports to localhost: all, or those you name",
		Long: "Forwards ports from the host to 127.0.0.1 on this machine, through an SSH connection of its own,\n" +
			"until you press Ctrl+C. Write local:remote to use another local port. Nothing is exposed beyond this machine.\n" +
			"It refuses to start when your SSH config has a RemoteForward, a Tunnel or ForkAfterAuthentication yes for the host.",
		Example: "  " + brand.Name + " forward my-vps/auth-fix 3000\n  " + brand.Name + " forward my-vps/auth-fix 8080:3000",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var h client.Host
			var pairs []client.ForwardPair
			err := withSession(cmd, args[0], func(ctx context.Context, hh client.Host, conn *client.Conn, s *client.Session) error {
				h = hh
				var err error
				pairs, err = forwardPairs(ctx, conn, h, s, args[1:])
				return err
			})
			if err != nil {
				return err
			}
			return runForward(cmd.Context(), h, pairs)
		},
	}
}

// sessionPorts asks a host for the ports that the session id listens on.
// Tests replace it.
var sessionPorts = func(ctx context.Context, conn *client.Conn, id string) ([]client.Port, error) {
	return conn.Ports(ctx, id)
}

// forwardPairs turns the ports asked for (all the session's ports when none
// are) into pairs of a local and a remote port.
func forwardPairs(ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, want []string) ([]client.ForwardPair, error) {
	ports, err := sessionPorts(ctx, conn, s.ID)
	if err != nil {
		return nil, err
	}
	addr := map[int]string{}
	for _, p := range ports {
		addr[p.Port] = p.Address
	}
	if len(want) == 0 {
		for _, p := range ports {
			want = append(want, strconv.Itoa(p.Port))
		}
	}
	if len(want) == 0 {
		return nil, &client.UserError{Msg: fmt.Sprintf("%s on %s listens on no TCP ports yet", shown(nameOf(*s)), shown(h.Name)), Hint: "Start the dev server in the session, or give the port.", Code: "no_ports"}
	}
	pairs := make([]client.ForwardPair, 0, len(want))
	for _, w := range want {
		l, r, ok := strings.Cut(w, ":")
		if !ok {
			r = l
		}
		lp, err1 := strconv.Atoi(l)
		rp, err2 := strconv.Atoi(r)
		if err1 != nil || err2 != nil {
			return nil, notAPort(h.Name, s, w)
		}
		if !ok && !free(ctx, lp) {
			lp = freePort(ctx)
		}
		pairs = append(pairs, client.ForwardPair{Local: lp, RemoteHost: remoteHost(addr[rp]), Remote: rp})
	}
	return pairs, nil
}

// notAPort is the error of w, a word of forward that is neither a port nor
// a pair. Its Try: line forwards a port of the session s on host, by
// host/ID.
func notAPort(host string, s *client.Session, w string) error {
	return &client.UserError{Msg: fmt.Sprintf("%q is not a port or local:remote pair", w), Hint: sessionCommand(host, s.ID, "forward", "3000"), Code: "bad_value", Kind: client.KindUsage}
}

// runForward forwards the pairs through ssh until Ctrl+C. With input off,
// ssh asks nothing, and does not get the terminal.
func runForward(ctx context.Context, h client.Host, pairs []client.ForwardPair) error {
	if h.SSH == "" {
		return &client.UserError{Msg: fmt.Sprintf("forward needs SSH, and %s is reached through %s", h.Name, h.Target()), Hint: "Forward from inside the host instead, for example with the provider's own port tools.", Code: "needs_ssh"}
	}
	s := client.NewSSH(ctx, h, true)
	var stdin io.Reader
	if client.InputAllowed(ctx) {
		stdin = os.Stdin
	}
	err := s.RunForward(ctx, pairs, stdin, os.Stdout, os.Stderr, func() {
		for _, p := range pairs {
			fmt.Fprintf(os.Stderr, "Forwarding http://localhost:%d → %s:%d\n", p.Local, h.Name, p.Remote)
		}
		fmt.Fprintln(os.Stderr, "Press Ctrl+C to stop.")
	})
	var ue *client.UserError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ue) {
		return err
	}
	if err != nil {
		return &client.UserError{Msg: "the forward stopped: " + err.Error(), Hint: "Check that the local port is free, then try again.", Code: "forward_failed", Kind: client.KindFailed}
	}
	return nil
}

func remoteHost(addr string) string {
	switch addr {
	case "::", "::1":
		return "[::1]"
	case "", "0.0.0.0", "127.0.0.1":
		return "127.0.0.1"
	}
	if strings.Contains(addr, ":") {
		return "[" + addr + "]"
	}
	return addr
}

func free(ctx context.Context, port int) bool {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func freePort(ctx context.Context) int {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

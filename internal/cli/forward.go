package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func portsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports <host>/<session>",
		Short: "List the TCP ports a session's processes listen on",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				ports, err := conn.Ports(ctx, s.ID)
				if err != nil {
					return err
				}
				if len(ports) == 0 {
					fmt.Printf("%s on %s listens on no TCP ports.\n", s.Name, h.Name)
					return nil
				}
				for _, p := range ports {
					fmt.Printf("%-6d %-16s %s (pid %d)\n", p.Port, p.Address, p.Process, p.PID)
				}
				return nil
			})
		},
	}
}

func forwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forward <host>/<session> [port…]",
		Short: "Forward a session's ports to localhost (all of them if none are given)",
		Long: "Forwards ports from the host to 127.0.0.1 on this machine, through an SSH connection of its own,\n" +
			"until you press Ctrl+C. Write local:remote to use another local port. Nothing is exposed beyond this machine.\n" +
			"It refuses to start when your SSH config has a RemoteForward, a Tunnel or ForkAfterAuthentication yes for the host.",
		Example: "  " + brand.Name + " forward my-vps/web 3000\n  " + brand.Name + " forward my-vps/web 8080:3000",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var h client.Host
			var pairs []client.ForwardPair
			err := withSession(cmd.Context(), args[0], func(ctx context.Context, hh client.Host, conn *client.Conn, s *client.Session) error {
				h = hh
				var err error
				pairs, err = forwardPairs(ctx, conn, h, s, args[0], args[1:])
				return err
			})
			if err != nil {
				return err
			}
			return runForward(cmd.Context(), h, pairs)
		},
	}
}

// forwardPairs turns the ports asked for (all the session's ports when none
// are) into pairs of a local and a remote port.
func forwardPairs(ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, target string, want []string) ([]client.ForwardPair, error) {
	ports, err := conn.Ports(ctx, s.ID)
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
		return nil, &client.UserError{Msg: fmt.Sprintf("%s on %s listens on no TCP ports yet", s.Name, h.Name), Hint: "Start the dev server in the session, or give the port."}
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
			return nil, &client.UserError{Msg: fmt.Sprintf("%q is not a port or local:remote pair", w), Hint: brand.Name + " forward " + target + " 3000"}
		}
		if !ok && !free(ctx, lp) {
			lp = freePort(ctx)
		}
		pairs = append(pairs, client.ForwardPair{Local: lp, RemoteHost: remoteHost(addr[rp]), Remote: rp})
	}
	return pairs, nil
}

// runForward forwards the pairs through ssh until Ctrl+C.
func runForward(ctx context.Context, h client.Host, pairs []client.ForwardPair) error {
	if h.SSH == "" {
		return &client.UserError{Msg: fmt.Sprintf("forward needs SSH, and %s is reached through %s", h.Name, h.Target()), Hint: "Forward from inside the host instead, for example with the provider's own port tools."}
	}
	s := &client.SSH{Host: h, Interactive: true}
	c, err := s.Forward(ctx, pairs)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		fmt.Printf("Forwarding http://localhost:%d → %s:%d\n", p.Local, h.Name, p.Remote)
	}
	fmt.Println("Press Ctrl+C to stop.")
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil && ctx.Err() == nil {
		return &client.UserError{Msg: "the forward stopped: " + err.Error(), Hint: "Check that the local port is free, then try again."}
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

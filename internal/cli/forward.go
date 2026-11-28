package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

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
				printPorts(os.Stdout, ports)
				return nil
			})
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

type forwardPlan struct {
	host         client.Host
	sessionID    string
	walled       bool
	firstForward bool
	ownedGrants  []int
	pairs        []client.ForwardPair
}

func forwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forward <host>/<session> [<port>…]",
		Short: "Forward a session's ports to localhost: all, or those you name",
		Long: "Forwards ports from the host to 127.0.0.1 on this machine, through an\n" +
			"SSH connection of its own, until you press Ctrl+C. Write local:remote to\n" +
			"use another local port. Nothing is exposed beyond this machine. For a\n" +
			"walled session, each named port opens a private Unix socket on the host\n" +
			"while forwarded, and omitting ports asks which claimed port to forward.\n" +
			"It refuses to start when your SSH config has a RemoteForward, a Tunnel\n" +
			"or ForkAfterAuthentication yes for the host.",
		Example: "  " + brand.Name + " forward my-vps/auth-fix 3000\n  " + brand.Name + " forward my-vps/auth-fix 8080:3000",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var plan forwardPlan
			err := withSession(cmd, args[0], func(ctx context.Context, hh client.Host, conn *client.Conn, s *client.Session) error {
				var err error
				plan, err = buildForwardPlan(cmd, ctx, conn, hh, s, args[0], args[1:])
				return err
			})
			if err != nil {
				return err
			}
			if len(plan.pairs) == 0 {
				return nil
			}
			defer revokeForwardGrants(cmd.Context(), plan.host, plan.sessionID, plan.ownedGrants)
			return runForward(cmd.Context(), plan)
		},
	}
}

// sessionPorts asks a host for the ports that the session id listens on.
// Tests replace it.
var sessionPorts = func(ctx context.Context, conn *client.Conn, id string) ([]client.Port, error) {
	return conn.Ports(ctx, id)
}

func buildForwardPlan(cmd *cobra.Command, ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, target string, want []string) (forwardPlan, error) {
	if s.Wall != nil {
		return walledForwardPlan(cmd, ctx, conn, h, s, target, want)
	}
	pairs, err := forwardPairs(ctx, conn, h, s, target, want)
	if err != nil {
		return forwardPlan{}, err
	}
	return forwardPlan{host: h, sessionID: s.ID, pairs: pairs}, nil
}

// forwardPairs turns the ports asked for (all the session's ports when none
// are) into pairs of a local and a remote port for an unwalled session.
func forwardPairs(ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, _ string, want []string) ([]client.ForwardPair, error) {
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
		return nil, noPortsError(h, s)
	}
	pairs := make([]client.ForwardPair, 0, len(want))
	for _, w := range want {
		lp, rp, explicitLocal, err := parsePortPair(w, h.Name, s)
		if err != nil {
			return nil, err
		}
		if !explicitLocal && !free(ctx, lp) {
			lp = freePort(ctx)
		}
		pairs = append(pairs, client.ForwardPair{Local: lp, RemoteHost: remoteHost(addr[rp]), Remote: rp})
	}
	return pairs, nil
}

// walledForwardPlan builds a forward plan for a walled session (spec 7.4).
// It never auto-forwards guest-claimed ports without an explicit choice, and
// asks the supervisor to open a private host Unix socket for each port.
func walledForwardPlan(cmd *cobra.Command, ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, target string, want []string) (forwardPlan, error) {
	if h.SSH == "" {
		return forwardPlan{}, needsSSHError(h)
	}
	if !conn.HasFeature("port.grant") {
		return forwardPlan{}, &client.UserError{
			Msg:  fmt.Sprintf("the helper on %s is too old to forward ports for walled sessions", h.Name),
			Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
			Code: "wall_helper_old",
		}
	}
	if len(want) == 0 {
		var err error
		want, err = chooseWalledPorts(cmd, ctx, conn, h, s, target)
		if err != nil || len(want) == 0 {
			return forwardPlan{}, err
		}
	}
	parsed := make([]struct {
		lp, rp   int
		explicit bool
	}, 0, len(want))
	for _, w := range want {
		lp, rp, explicit, err := parsePortPair(w, h.Name, s)
		if err != nil {
			return forwardPlan{}, err
		}
		parsed = append(parsed, struct {
			lp, rp   int
			explicit bool
		}{lp: lp, rp: rp, explicit: explicit})
	}
	return grantWalledPairs(ctx, conn, h, s.ID, parsed)
}

func chooseWalledPorts(cmd *cobra.Command, ctx context.Context, conn *client.Conn, h client.Host, s *client.Session, target string) ([]string, error) {
	ports, err := sessionPorts(ctx, conn, s.ID)
	if err != nil {
		return nil, err
	}
	if len(ports) == 0 {
		return nil, noPortsError(h, s)
	}
	p := prompts(cmd)
	if !p.on {
		return nil, &client.UserError{
			Msg:  fmt.Sprintf("forward needs a port for walled session %s on %s (%s), and input is off", shown(nameOf(*s)), shown(h.Name), summarizeClaimedPorts(ports)),
			Hint: fmt.Sprintf("%s forward %s %d", brand.Name, shellWord(target), ports[0].Port),
			Code: "needs_answer",
			Kind: client.KindNeedsAnswer,
		}
	}
	return askWalledPorts(p, h, s, target, ports)
}

func askWalledPorts(p *prompter, h client.Host, s *client.Session, _ string, ports []client.Port) ([]string, error) {
	fmt.Fprintf(p.out, "Ports in %s on %s:\n", shown(nameOf(*s)), shown(h.Name))
	for i, pt := range ports {
		proc := client.CleanLine(pt.Process)
		if proc == "" {
			proc = "-"
		}
		fmt.Fprintf(p.out, "  %d) %-6d %-16s %s [%s]\n", i+1, pt.Port, client.CleanLine(pt.Address), proc, portStateTag(pt))
	}
	ans, err := p.ask("Forward which port(s)? (number, port, or Enter for none): ")
	if err != nil {
		return nil, err
	}
	if ans == "" {
		fmt.Fprintln(p.out, "No ports forwarded.")
		return nil, nil
	}
	return resolvePortSelection(ans, h.Name, s, ports)
}

func resolvePortSelection(ans, host string, s *client.Session, ports []client.Port) ([]string, error) {
	fields := strings.FieldsFunc(ans, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' })
	var out []string
	for _, f := range fields {
		if !strings.Contains(f, ":") {
			if idx, err := strconv.Atoi(f); err == nil && idx >= 1 && idx <= len(ports) {
				out = append(out, strconv.Itoa(ports[idx-1].Port))
				continue
			}
		}
		if _, _, _, err := parsePortPair(f, host, s); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func grantWalledPairs(ctx context.Context, conn *client.Conn, h client.Host, sessionID string, parsed []struct {
	lp, rp   int
	explicit bool
}) (forwardPlan, error) {
	plan := forwardPlan{host: h, sessionID: sessionID, walled: true}
	for _, item := range parsed {
		grant, err := conn.GrantPort(ctx, sessionID, item.rp)
		if err != nil {
			revokeWithConn(ctx, conn, sessionID, plan.ownedGrants)
			return forwardPlan{}, err
		}
		if !grant.AlreadyGranted {
			plan.ownedGrants = append(plan.ownedGrants, item.rp)
		}
		if grant.FirstForward {
			plan.firstForward = true
		}
		lp := item.lp
		if !item.explicit && !free(ctx, lp) {
			lp = freePort(ctx)
		}
		plan.pairs = append(plan.pairs, client.ForwardPair{Local: lp, Remote: item.rp, RemoteSocket: grant.Socket})
	}
	return plan, nil
}

func revokeWithConn(ctx context.Context, conn *client.Conn, sessionID string, ports []int) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, port := range ports {
		_ = conn.RevokePort(rctx, sessionID, port)
	}
}

func revokeForwardGrants(ctx context.Context, h client.Host, sessionID string, ports []int) {
	if len(ports) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	conn, err := client.Dial(rctx, h, false)
	if err != nil {
		return
	}
	defer conn.Close()
	for _, port := range ports {
		_ = conn.RevokePort(rctx, sessionID, port)
	}
}

func parsePortPair(w, host string, s *client.Session) (lp, rp int, explicitLocal bool, err error) {
	l, r, ok := strings.Cut(w, ":")
	if !ok {
		r = l
	}
	lp, err1 := strconv.Atoi(l)
	rp, err2 := strconv.Atoi(r)
	if err1 != nil || err2 != nil || lp < 1 || lp > 65535 || rp < 1 || rp > 65535 {
		return 0, 0, false, notAPort(host, s, w)
	}
	return lp, rp, ok, nil
}

// notAPort is the error of w, a word of forward that is neither a port nor
// a pair. Its Try: line forwards a port of the session s on host, by
// host/ID.
func notAPort(host string, s *client.Session, w string) error {
	return &client.UserError{Msg: fmt.Sprintf("%q is not a port or local:remote pair", w), Hint: sessionCommand(host, s.ID, "forward", "3000"), Code: "bad_value", Kind: client.KindUsage}
}

func noPortsError(h client.Host, s *client.Session) error {
	return &client.UserError{
		Msg:  fmt.Sprintf("%s on %s listens on no TCP ports yet", shown(nameOf(*s)), shown(h.Name)),
		Hint: "Start the dev server in the session, or give the port.",
		Code: "no_ports",
	}
}

func needsSSHError(h client.Host) error {
	return &client.UserError{
		Msg:  fmt.Sprintf("forward needs SSH, and %s is reached through %s", h.Name, h.Target()),
		Hint: "Forward from inside the host instead, for example with the provider's own port tools.",
		Code: "needs_ssh",
	}
}

func summarizeClaimedPorts(ports []client.Port) string {
	nums := make([]string, len(ports))
	for i, p := range ports {
		nums[i] = strconv.Itoa(p.Port)
	}
	return "claimed: " + strings.Join(nums, ", ")
}

// runForward forwards the pairs through ssh until Ctrl+C. With input off,
// ssh asks nothing, and does not get the terminal.
func runForward(ctx context.Context, plan forwardPlan) error {
	if plan.host.SSH == "" {
		return needsSSHError(plan.host)
	}
	s := client.NewSSH(ctx, plan.host, true)
	c, err := s.Forward(ctx, plan.pairs)
	if err != nil {
		return err
	}
	for _, p := range plan.pairs {
		fmt.Fprintf(os.Stderr, "Forwarding http://localhost:%d → %s:%d\n", p.Local, plan.host.Name, p.Remote)
	}
	if plan.walled && plan.firstForward {
		fmt.Fprintf(os.Stderr, "Note: localhost shares cookies with other local apps across ports. Use http://%s.localhost:%d for a separate origin.\n", plan.sessionID, plan.pairs[0].Local)
	}
	fmt.Fprintln(os.Stderr, "Press Ctrl+C to stop.")
	if client.InputAllowed(ctx) {
		c.Stdin = os.Stdin
	}
	var errb bytes.Buffer
	c.Stdout, c.Stderr = os.Stdout, io.MultiWriter(os.Stderr, &errb)
	if err := c.Run(); err != nil && ctx.Err() == nil {
		return client.ForwardError(plan.host, err, errb.String())
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

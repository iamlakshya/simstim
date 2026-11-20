package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func (g *guide) findKey(m *Model) tea.Cmd {
	if g.run || g.step != gdRentKey {
		return nil
	}
	scope := g.nextScope()
	local := m.be.Local()
	return m.bgCmd(func(context.Context) tea.Msg { return rentKeyMsg{scope: scope, key: local.PublicKey()} })
}

func (g *guide) keyKey(m *Model, ks string) tea.Cmd {
	switch ks {
	case "enter":
		if g.plan.Key == "" {
			return g.findKey(m)
		}
		g.step, g.in = gdRentServer, newInput(g.plan.Addr, "203.0.113.10")
	case "k":
		if g.plan.Key != "" {
			return nil
		}
		ctx := m.ctx
		// ssh-keygen asks its own questions, so it gets the terminal.
		x := &funcExec{run: func(in, out *os.File) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// No timeout: the person answers ssh-keygen's questions.
			cmd := exec.CommandContext(ctx, onboard.KeyArgs[0], onboard.KeyArgs[1:]...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
			fmt.Fprintf(out, "\r\n%s\r\n\r\n", onboard.KeyCommand)
			return cmd.Run()
		}}
		scope := g.nextScope()
		local := m.be.Local()
		return m.execWithInput(x, func(err error) tea.Msg {
			return rentKeyMsg{scope: scope, key: local.PublicKey(), err: err}
		})
	}
	return nil
}

// checkUser checks that ssh reaches the new user, and goes on when it does.
func (g *guide) checkUser(m *Model) tea.Cmd {
	u := strings.TrimSpace(g.in.value())
	if u == "" {
		u = onboard.RentUser
	}
	if !onboard.ValidUser(u) {
		g.err = &client.UserError{Msg: fmt.Sprintf("%q cannot name a user", u), Hint: "Use lower-case letters, digits, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
		return nil
	}
	g.plan.User = u
	return g.sshCheck(m, g.plan.Login())
}

func (g *guide) sshCheck(m *Model, target string) tea.Cmd {
	g.checks = []check{{name: "ssh", state: ckRunning, detail: target}}
	ctx, scope := g.begin(m)
	local, clock := m.be.Local(), m.now
	return m.bgCmd(func(context.Context) tea.Msg {
		start := clock()
		err := local.SSHWorks(ctx, target)
		return rentCheckMsg{scope: scope, err: err, took: clock().Sub(start)}
	})
}

func (g *guide) onRentCheck(m *Model, msg rentCheckMsg) tea.Cmd {
	if !g.owns(msg.scope) || !g.run || (g.step != gdRentUser && g.step != gdRentConfig) {
		return nil
	}
	g.seq++
	g.finishOp()
	if msg.err != nil {
		g.checks[0].state = ckFailed
		g.err = msg.err
		return nil
	}
	g.checks[0].state, g.checks[0].took = ckOK, msg.took
	if g.step == gdRentUser {
		g.step, g.checks = gdRentName, nil
		g.in = newInput(g.nameOr(), onboard.RentName)
		return nil
	}
	// The name reaches the server: the wizard connects it.
	h := client.Host{Name: g.plan.Name, SSH: g.plan.Name}
	w := newWizard()
	w.guided = true
	w.host = h
	w.in[0].set(h.SSH)
	w.in[1].set(h.Name)
	m.modal = w
	return w.probe(m)
}

func (g *guide) nameOr() string {
	if g.plan.Name != "" {
		return g.plan.Name
	}
	return onboard.RentName
}

func (g *guide) nameServer(m *Model) tea.Cmd {
	name := strings.TrimSpace(g.in.value())
	if name == "" {
		name = onboard.RentName
	}
	switch {
	case !client.ValidHostName(name):
		g.err = &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
		return nil
	case m.hostByName(name) != nil:
		g.err = &client.UserError{Msg: "there is already a host named " + name, Hint: "Pick another name.", Code: "host_exists"}
		return nil
	}
	g.plan.Name, g.err = name, nil
	scope := g.nextScope()
	local := m.be.Local()
	return m.bgCmd(func(context.Context) tea.Msg { return rentNameMsg{scope: scope, present: local.HasSSHHost(name)} })
}

func (g *guide) configKey(m *Model, ks string) tea.Cmd {
	switch {
	case ks == "y" && !g.present && !g.asked:
		local, lines := m.be.Local(), g.plan.ConfigLines()
		if err := local.AddSSHHost(lines); err != nil {
			g.err = &client.UserError{Msg: "could not add the lines to ~/.ssh/config: " + err.Error(), Hint: "Add them yourself, then press enter.", Code: "ssh_config_write"}
			g.asked = true
			return nil
		}
		g.present = true
		g.note = "Added to ~/.ssh/config."
		return g.sshCheck(m, g.plan.Name)
	case ks == "n" && !g.present:
		g.asked = true
	case ks == "enter" && (g.present || g.asked):
		return g.sshCheck(m, g.plan.Name)
	}
	return nil
}

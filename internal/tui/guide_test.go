package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func agentReady(kind, title, login string) client.AgentCheck {
	return client.AgentCheck{Kind: kind, Title: title, Installed: true, OnPath: true, Path: "/home/dev/.local/bin/" + kind, Login: login}
}

// installs makes the fake host install each agent its sessions name, and
// log in to each agent its login sessions name: a stand-in for the
// installer and the user.
func installs(be *fakeBackend) {
	be.attachHook = func(o client.AttachOptions) {
		st := be.state[o.Host.Name]
		for _, s := range st.sessions {
			if s.ID != o.Session {
				continue
			}
			kind := s.Name[strings.Index(s.Name, "-")+1:]
			switch {
			case strings.HasPrefix(s.Name, "install-"):
				a, _ := agents.Find(kind)
				st.checks = append(st.checks, agentReady(kind, a.Title, api.LoginNo))
			case strings.HasPrefix(s.Name, "login-"):
				for i := range st.checks {
					if st.checks[i].Kind == kind {
						st.checks[i].Login = api.LoginYes
					}
				}
			}
		}
	}
}

func hasCallPrefix(t *testing.T, be *fakeBackend, prefix string) {
	t.Helper()
	for _, c := range be.callList() {
		if strings.HasPrefix(c, prefix) {
			return
		}
	}
	t.Fatalf("no call starts with %q in %q", prefix, be.callList())
}

func TestGuideFromTheWelcome(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	l := be.Local().(*fakeLocal)
	l.has["limactl"] = false
	hs := newHarness(t, be, 80, 24)
	if out := plainScreen(hs.m); !strings.Contains(out, "enter set up") {
		t.Fatalf("the welcome does not offer the setup:\n%s", out)
	}
	hs.key("enter")
	g, ok := hs.m.modal.(*guide)
	if !ok || len(g.places) != 5 {
		t.Fatalf("enter opened %T", hs.m.modal)
	}
	// Lima is not ready: its steps show, and enter checks again.
	hs.key("down")
	if out := plainScreen(hs.m); !strings.Contains(out, "brew install lima") || !strings.Contains(out, "enter check again") {
		t.Fatalf("no steps for lima:\n%s", out)
	}
	hs.key("enter")
	if msg, _ := errText(g.err); !strings.Contains(msg, "not ready yet") || g.step != gdWhere {
		t.Fatalf("a place that is still not ready: %v, step %v", g.err, g.step)
	}
	l.mu.Lock()
	l.has["limactl"] = true
	l.mu.Unlock()
	hs.key("enter")
	if g.step != gdName || g.kind != "lima" || g.err != nil {
		t.Fatalf("lima ready: step %v kind %q err %v", g.step, g.kind, g.err)
	}
	hs.key("ctrl+u")
	hs.typeText("vm")
	installs(be)
	hs.key("enter")
	hasCall(t, be, "create lima vm")
	w, ok := hs.m.modal.(*wizard)
	if !ok {
		t.Fatalf("after the create: %T", hs.m.modal)
	}
	// Making it was the yes: the helper installed without a question, and
	// the VM got its idle stop.
	hasCall(t, be, "install vm")
	hasCall(t, be, "idle vm")
	if w.step != wzReady || !w.saved || w.flow == nil {
		t.Fatalf("not ready: step %v saved %v flow %v err %v", w.step, w.saved, w.flow != nil, w.err)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "idle stop") || !strings.Contains(out, "enter choose agents") {
		t.Fatalf("ready:\n%s", out)
	}

	hs.key("enter")
	if w.step != wzAgents {
		t.Fatalf("the checklist does not follow: %v", w.step)
	}
	hs.key("down", "space") // Codex
	hs.key("enter")
	if w.step != wzRun || w.act.Agent.Kind != "codex" {
		t.Fatalf("install step: %v %+v", w.step, w.act)
	}
	hs.key("enter")
	hasCallPrefix(t, be, "new vm agent= argv=[/bin/sh -c curl -fsSL https://chatgpt.com/codex/install.sh | sh] cwd=~ name=install-codex")
	if len(be.attached) != 1 {
		t.Fatalf("the installer was not attached: %d", len(be.attached))
	}
	// Codex installed but not logged in: its login is offered.
	if w.step != wzRun || w.act.Kind != "login" || w.last == nil || w.last.Title != "Codex" {
		t.Fatalf("after the install: step %v act %+v last %+v err %v", w.step, w.act, w.last, w.err)
	}
	hs.key("enter")
	hasCallPrefix(t, be, "new vm agent= argv=[/bin/sh -c codex login --device-auth] cwd=~ name=login-codex")
	if w.step != wzDone {
		t.Fatalf("after the login: %v %+v", w.step, w.act)
	}
	out := plainScreen(hs.m)
	if !strings.Contains(out, "vm is set up") || !strings.Contains(out, brand.Name+" new vm --agent codex") {
		t.Fatalf("summary:\n%s", out)
	}
	hs.key("enter")
	d, ok := hs.m.modal.(*newSession)
	if !ok || d.host != "vm" || hs.m.lastAgent["vm"] != "codex" {
		t.Fatalf("the first session: %T %+v", hs.m.modal, hs.m.lastAgent)
	}
}

func TestGuideRent(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "amd64"}
	l := be.Local().(*fakeLocal)
	l.sshErr = map[string]error{"dev@203.0.113.10": &client.UserError{Msg: "cannot log in to dev@203.0.113.10 with ssh yet: Permission denied (publickey).", Hint: "ssh dev@203.0.113.10"}}
	hs := newHarness(t, be, 100, 30)
	hs.key("s", "5")
	g := hs.m.modal.(*guide)
	if g.step != gdRentKey || g.plan.Key != "" {
		t.Fatalf("rent: step %v key %q", g.step, g.plan.Key)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "k make it now") {
		t.Fatalf("no key:\n%s", out)
	}
	l.mu.Lock()
	l.key = "~/.ssh/id_ed25519.pub"
	l.mu.Unlock()
	hs.key("enter") // check again: found
	hs.key("enter")
	if g.step != gdRentServer {
		t.Fatalf("step %v", g.step)
	}
	hs.typeText("not an address")
	hs.key("enter")
	if g.err == nil || g.step != gdRentServer {
		t.Fatal("a bad address went through")
	}
	hs.key("ctrl+u")
	hs.typeText("203.0.113.10")
	hs.key("enter", "enter")
	if g.step != gdRentUser || g.err == nil {
		t.Fatalf("the failed ssh check: step %v err %v", g.step, g.err)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "Permission denied (publickey)") {
		t.Fatalf("the error is hidden:\n%s", out)
	}
	l.mu.Lock()
	l.sshErr = nil
	l.mu.Unlock()
	hs.key("enter")
	if g.step != gdRentName {
		t.Fatalf("step %v, err %v", g.step, g.err)
	}
	hs.key("enter")
	if g.step != gdRentConfig || g.present {
		t.Fatalf("config: step %v present %v", g.step, g.present)
	}
	out := plainScreen(hs.m)
	for _, want := range []string{"Host my-server", "HostName 203.0.113.10", "User dev", "y add them"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if len(l.added) != 0 {
		t.Fatal("the config changed before the yes")
	}
	hs.key("y")
	if len(l.added) != 1 || l.added[0] != "Host my-server\n    HostName 203.0.113.10\n    User dev\n" {
		t.Fatalf("added %q", l.added)
	}
	hasCall(t, be, "ssh check my-server")
	w, ok := hs.m.modal.(*wizard)
	if !ok || w.host.Name != "my-server" || w.host.SSH != "my-server" || w.step != wzTest {
		t.Fatalf("the hand-over: %T %+v", hs.m.modal, w)
	}
}

func TestGuideStopsACreate(t *testing.T) {
	be := newFakeBackend()
	l := be.Local().(*fakeLocal)
	l.block = true
	hs := newHarness(t, be, 80, 24)
	hs.key("enter", "3", "enter")
	g := hs.m.modal.(*guide)
	if g.step != gdCreate || !g.run {
		t.Fatalf("not creating: %v", g.step)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "esc stop") {
		t.Fatalf("no way to stop:\n%s", out)
	}
	hs.key("esc")
	hasCall(t, be, "create stopped, clean-up done")
	if g.run || g.step != gdCreate {
		t.Fatalf("still running: %v", g.run)
	}
	if msg, _ := errText(g.err); !strings.Contains(msg, "half-made docker machine") {
		t.Fatalf("err = %v", g.err)
	}
	hs.key("enter")
	if g.step != gdName {
		t.Fatalf("try again: %v", g.step)
	}
}

func TestGuideQuitWaitsForTheCleanUp(t *testing.T) {
	be := newFakeBackend()
	l := be.Local().(*fakeLocal)
	l.block = true
	hs := newHarness(t, be, 80, 24)
	hs.key("enter", "3", "enter")
	hs.m.Close()
	hasCall(t, be, "create stopped, clean-up done")
}

func TestGuideBackFromSSH(t *testing.T) {
	be := newFakeBackend()
	be.Local().(*fakeLocal).sshHosts = []string{"atlas-dev"}
	hs := newHarness(t, be, 80, 24)
	hs.key("enter", "enter")
	w, ok := hs.m.modal.(*wizard)
	if !ok || !w.guided || !slices.Equal(w.known, []string{"atlas-dev"}) {
		t.Fatalf("ssh opened %T", hs.m.modal)
	}
	hs.key("esc")
	if _, ok := hs.m.modal.(*guide); !ok {
		t.Fatalf("esc went to %T", hs.m.modal)
	}
	hs.key("esc")
	if hs.m.modal != nil {
		t.Fatal("esc did not close the setup")
	}
}

func TestWizardAgentsOnlyWhenNeeded(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.installed = true
	be.state["pi"] = &fakeHost{info: client.HostInfo{Distro: "Debian 13", Agents: map[string]string{"shell": "/bin/bash"}}, checks: []client.AgentCheck{
		agentReady("claude", "Claude Code", api.LoginYes), agentReady("codex", "Codex", api.LoginYes),
		agentReady("opencode", "opencode", api.LoginYes), agentReady("antigravity", "Antigravity CLI", api.LoginUnknown),
	}}
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi")
	hs.key("enter", "enter")
	w := hs.m.modal.(*wizard)
	if !w.saved || w.wantsAgents() {
		t.Fatalf("saved %v, wants agents %v", w.saved, w.wantsAgents())
	}
	hs.key("enter")
	if _, ok := hs.m.modal.(*newSession); !ok {
		t.Fatalf("with every agent ready, enter opened %T", hs.m.modal)
	}
}

func TestWizardAgentsSkipAndStop(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.installed = true
	be.state["pi"] = &fakeHost{checks: []client.AgentCheck{agentReady("claude", "Claude Code", api.LoginNo)}}
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi")
	hs.key("enter", "enter", "enter")
	w := hs.m.modal.(*wizard)
	// The fold opens on enter; space ticks a best-effort agent.
	hs.key("down", "down", "down", "down", "enter")
	if !w.unfold {
		t.Fatal("the fold did not open")
	}
	hs.key("space", "enter")
	if w.act.Kind != "install" || w.act.Agent.Kind != "gemini" {
		t.Fatalf("act %+v", w.act)
	}
	hs.key("s")
	if w.act.Kind != "login" || w.act.Agent.Kind != "claude" {
		t.Fatalf("after a skip: %+v", w.act)
	}
	hs.key("esc")
	if w.step != wzDone {
		t.Fatalf("esc did not stop: %v", w.step)
	}
	for _, c := range be.callList() {
		if strings.HasPrefix(c, "new pi") {
			t.Fatalf("a session started: %s", c)
		}
	}
	hs.key("esc")
	if hs.m.modal != nil {
		t.Fatal("esc did not close the summary")
	}
}

// agentsHarness is the wizard at "ready" on a new host pi, which has
// Claude Code, logged in, and no other agent.
func agentsHarness(t *testing.T, w, h int) *harness {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.installed = true
	be.state["pi"] = &fakeHost{info: client.HostInfo{Distro: "Debian 13", Arch: "arm64", CPUs: 4, MemTotal: 8 << 30, MemoryMax: 6551502848,
		Agents: map[string]string{"claude": "/home/dev/.local/bin/claude", "shell": "/bin/bash"}},
		checks: []client.AgentCheck{agentReady("claude", "Claude Code", api.LoginYes)}}
	installs(be)
	hs := newHarness(t, be, w, h)
	hs.m.motion = false
	hs.key("a")
	hs.typeText("pi")
	hs.key("enter", "enter")
	return hs
}

func TestGoldenGuide(t *testing.T) {
	views := []struct {
		name  string
		setup func(t *testing.T, w, h int) *Model
	}{
		{"guide_where", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			l := be.Local().(*fakeLocal)
			l.has["limactl"] = false
			l.sshHosts = []string{"atlas-dev", "gcp-dev", "hetzner-1"}
			hs := newHarness(t, be, w, h)
			hs.key("enter", "down")
			return hs.m
		}},
		{"guide_create", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.Local().(*fakeLocal).block = true
			hs := newHarness(t, be, w, h)
			hs.key("enter", "3", "enter")
			return hs.m
		}},
		{"guide_rent_user", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			l := be.Local().(*fakeLocal)
			l.key = "~/.ssh/id_ed25519.pub"
			l.sshErr = map[string]error{"dev@203.0.113.10": &client.UserError{Msg: "cannot log in to dev@203.0.113.10 with ssh yet: Permission denied (publickey).", Hint: "ssh dev@203.0.113.10   (run it yourself to see what it asks)"}}
			hs := newHarness(t, be, w, h)
			hs.key("enter", "5", "enter")
			hs.typeText("203.0.113.10")
			hs.key("enter", "enter")
			return hs.m
		}},
		{"guide_rent_config", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.Local().(*fakeLocal).key = "~/.ssh/id_ed25519.pub"
			hs := newHarness(t, be, w, h)
			hs.key("enter", "5", "enter")
			hs.typeText("203.0.113.10")
			hs.key("enter", "enter", "enter")
			return hs.m
		}},
		{"wizard_agents", func(t *testing.T, w, h int) *Model {
			hs := agentsHarness(t, w, h)
			hs.key("enter", "down", "space")
			return hs.m
		}},
		{"wizard_install_agent", func(t *testing.T, w, h int) *Model {
			hs := agentsHarness(t, w, h)
			hs.key("enter", "down", "space", "enter")
			return hs.m
		}},
		{"wizard_login", func(t *testing.T, w, h int) *Model {
			hs := agentsHarness(t, w, h)
			hs.key("enter", "down", "space", "enter", "enter")
			return hs.m
		}},
		{"wizard_done", func(t *testing.T, w, h int) *Model {
			hs := agentsHarness(t, w, h)
			hs.key("enter", "down", "space", "enter", "enter", "s")
			return hs.m
		}},
	}
	for _, v := range views {
		for _, s := range sizes {
			name := fmt.Sprintf("%s_%dx%d", v.name, s.w, s.h)
			t.Run(name, func(t *testing.T) {
				golden(t, name, v.setup(t, s.w, s.h))
			})
		}
	}
}

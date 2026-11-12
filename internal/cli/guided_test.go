package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
)

// guideLocal is the setup's computer in tests: one fake place that becomes
// ready after a check, an SSH key, a config and ssh checks.
type guideLocal struct {
	checks   int // Places calls so far; the fake place is ready from the second
	key      string
	keyLater string // the key that shows from the second look
	looks    int
	sshFail  map[string]int // failures left, by target
	sshCalls []string
	added    []string
	named    map[string]bool
}

func (l *guideLocal) Places() []onboard.Place {
	l.checks++
	return []onboard.Place{
		{Kind: onboard.SSH, Title: "A host in your SSH config", Ready: true, Note: "none found"},
		{Kind: "fake", Title: "A fake machine", Ready: l.checks > 1, Note: "fakectl is not on your PATH",
			Steps: []onboard.Step{{Say: "Install it:", Run: "brew install fake"}}},
		{Kind: onboard.Rent, Title: "A server I will rent", Ready: true},
	}
}

func (l *guideLocal) PublicKey() string {
	l.looks++
	if l.looks > 1 && l.keyLater != "" {
		return l.keyLater
	}
	return l.key
}

func (l *guideLocal) HasSSHHost(name string) bool { return l.named[name] }

func (l *guideLocal) AddSSHHost(lines string) error {
	l.added = append(l.added, lines)
	return nil
}

func (l *guideLocal) SSHWorks(_ context.Context, target string) error {
	l.sshCalls = append(l.sshCalls, target)
	if l.sshFail[target] > 0 {
		l.sshFail[target]--
		return &client.UserError{Msg: "cannot log in to " + target + " with ssh yet: Permission denied (publickey).", Hint: "ssh " + target}
	}
	return nil
}

func (l *guideLocal) Create(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
	p, err := findProvider(kind)
	if err != nil {
		return client.Host{}, err
	}
	return onboard.Create(ctx, p, provider.Request{Name: name, Out: out})
}

func (l *guideLocal) SetIdle(context.Context, client.Host) (string, error) { return "", nil }

func readyCheck(kind, title, login string) api.AgentCheck {
	return api.AgentCheck{Kind: kind, Title: title, Installed: true, OnPath: true, Path: "/home/dev/.local/bin/" + kind, Login: login}
}

// testGuide is a guide that reads in and writes to out. Its sessions only
// record what they would run, and install what they are told to.
func testGuide(t *testing.T, ctx context.Context, o guideOpts, l onboard.Local, tr *clienttest.Transport, in string, tty bool) (*guide, *bytes.Buffer, *[]string) {
	t.Helper()
	var out bytes.Buffer
	g := newGuide(ctx, o, l)
	g.in, g.out, g.tty = strings.NewReader(in), &out, tty
	var ran []string
	g.session = func(h client.Host, a onboard.Action) (string, bool, error) {
		ran = append(ran, h.Name+" "+a.Args.Name)
		checks := slices.Clone(tr.Agents)
		switch a.Kind {
		case onboard.ActInstall:
			checks = append(checks, readyCheck(a.Agent.Kind, a.Agent.Title, api.LoginNo))
		case onboard.ActLogin:
			for i := range checks {
				if checks[i].Kind == a.Agent.Kind {
					checks[i].Login = api.LoginYes
				}
			}
		}
		tr.SetAgents(checks)
		return "s" + a.Args.Name, true, nil
	}
	return g, &out, &ran
}

func TestSetupWithFlags(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{readyCheck("claude", "Claude Code", api.LoginYes)})
	o := guideOpts{create: "fake", name: "box", agents: "codex", agentsSet: true, yes: true, noLogin: true}
	g, out, ran := testGuide(t, context.Background(), o, onboard.System{Providers: providers}, tr, "", false)
	if err := g.run(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !slices.Equal(*ran, []string{"box install-codex"}) {
		t.Fatalf("sessions = %v", *ran)
	}
	for _, want := range []string{
		"Creating box with fake…", "✓ created the fake machine fake-box",
		"[x] Claude Code", "[x] Codex", "To install: Codex.",
		"Codex (1 of 1), with its vendor's installer",
		"Done. box is ready.", "! Codex", "Try: " + brand.Name + " new box -- codex login --device-auth",
		brand.Name + " new box --agent claude",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "Log in now?") {
		t.Error("--no-login offered a login")
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the host was not saved")
	}
}

func TestSetupStepByStep(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{readyCheck("claude", "Claude Code", api.LoginNo)})
	l := &guideLocal{}
	// The fake place is not ready, then is; a name; tick Codex; log in to
	// Claude Code, and not to Codex.
	in := "2\n\nbox\n2\ny\nn\n"
	g, out, ran := testGuide(t, context.Background(), guideOpts{}, l, tr, in, true)
	if err := g.run(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !slices.Equal(*ran, []string{"box install-codex", "box login-claude"}) {
		t.Fatalf("sessions = %v\n%s", *ran, out)
	}
	for _, want := range []string{
		"1. Where will agents run?",
		"A fake machine is not ready on this computer: fakectl is not on your PATH.",
		"      brew install fake",
		"A name for the new host [fake]: ",
		"Type the numbers to install",
		"Claude Code is not logged in. Its login:",
		"Log in now? [Y/n]",
		"Skipped. Later: " + brand.Name + " new box -- codex login --device-auth",
		"✓ Claude Code", "Done. box is ready.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// cancelReader ends the setup's context when its answers run out: the user
// pressed Ctrl+C at that prompt.
type cancelReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c *cancelReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p[:1])
	if n == 0 {
		c.cancel()
		return 0, io.EOF
	}
	return n, err
}

func TestSetupStopsAndSaysWhatWasDone(t *testing.T) {
	_, tr := withFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, out, _ := testGuide(t, ctx, guideOpts{}, &guideLocal{}, tr, "", true)
	// The fake place is ready from the second look; the answers run out at
	// the checklist.
	g.in = &cancelReader{r: strings.NewReader("2\n\nbox\n"), cancel: cancel}
	err := g.run()
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "setup stopped; nothing else was changed" || !strings.Contains(ue.Hint, brand.Name+" setup") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{"Done before the stop:", "✓ made the fake machine fake-box, and saved the host box", "✓ saved the host box"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	g2, out2, _ := testGuide(t, ctx2, guideOpts{}, &guideLocal{}, tr, "", true)
	g2.in = &cancelReader{r: strings.NewReader(""), cancel: cancel2}
	if err := g2.run(); !errors.As(err, &ue) || ue.Msg != "setup stopped; nothing was changed" || ue.Kind != client.KindCancelled {
		t.Fatalf("a stop at the first question: %v\n%s", err, out2)
	}
}

func TestSetupNeedsATerminalOrFlags(t *testing.T) {
	withFake(t, nil)
	for _, o := range []guideOpts{{}, {host: "box"}, {create: "fake"}} {
		g, _, _ := testGuide(t, context.Background(), o, &guideLocal{}, &clienttest.Transport{}, "", false)
		err := g.run()
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "--yes") || ue.Kind != client.KindNeedsAnswer {
			t.Errorf("%+v: err = %v", o, err)
		}
	}
	for _, o := range []guideOpts{{host: "a", create: "fake", yes: true}, {create: "nope", yes: true}, {host: "a", agents: "nope", agentsSet: true, yes: true}} {
		g, _, _ := testGuide(t, context.Background(), o, &guideLocal{}, &clienttest.Transport{}, "", false)
		var ue *client.UserError
		if err := g.run(); !errors.As(err, &ue) || ue.Hint == "" {
			t.Errorf("%+v: err = %v", o, err)
		}
	}
}

func TestSetupNamedHost(t *testing.T) {
	withFake(t, nil)
	g, _, _ := testGuide(t, context.Background(), guideOpts{}, &guideLocal{}, &clienttest.Transport{}, "", false)
	h, err := g.namedHost("me@vps.example.com:2222")
	if err != nil || h.Name != "vps.example.com" || h.SSH != "me@vps.example.com:2222" {
		t.Fatalf("new host = %+v, %v", h, err)
	}
	saved := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	_ = client.SaveHost(saved)
	if h, err := g.namedHost("box"); err != nil || h.Machine == nil {
		t.Fatalf("a saved host = %+v, %v", h, err)
	}
	_ = client.SaveHost(client.Host{Name: "vps", SSH: "vps"})
	g.o.name = "vps"
	var ue *client.UserError
	if _, err := g.namedHost("me@203.0.113.9"); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "already a host called vps") {
		t.Fatalf("a clash: %v", err)
	}
}

func TestSetupRentWalkThrough(t *testing.T) {
	withFake(t, nil)
	l := &guideLocal{keyLater: "~/.ssh/id_ed25519.pub", sshFail: map[string]int{"dev@203.0.113.10": 1}}
	// No key: make it yourself, then Enter. A bad address, then a good one.
	// The default user. Enter when the user is made; the check fails, so
	// Enter again. The default name, and yes to the config lines.
	in := "n\n\nnot an address\n203.0.113.10\n\n\n\n\ny\n"
	g, out, _ := testGuide(t, context.Background(), guideOpts{}, l, &clienttest.Transport{}, in, true)
	h, err := g.rent()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if h.Name != onboard.RentName || h.SSH != onboard.RentName {
		t.Fatalf("host = %+v", h)
	}
	if !slices.Equal(l.sshCalls, []string{"dev@203.0.113.10", "dev@203.0.113.10", "my-server"}) {
		t.Fatalf("ssh checks = %v", l.sshCalls)
	}
	if len(l.added) != 1 || l.added[0] != "Host my-server\n    HostName 203.0.113.10\n    User dev\n" {
		t.Fatalf("added = %q", l.added)
	}
	for _, want := range []string{
		"Step 1 of 4: An SSH key", "ssh-keygen -t ed25519", "Run it now? [y/N]",
		"Step 2 of 4: A server", "cat ~/.ssh/id_ed25519.pub", "Type the address the console shows",
		"Step 3 of 4: A user for your agents", "      ssh root@203.0.113.10",
		"✕ Cannot log in to dev@203.0.113.10 with ssh yet", "✓ ssh dev@203.0.113.10 works",
		"Step 4 of 4: A name for the server", "        HostName 203.0.113.10",
		"Add them to the end of ~/.ssh/config? [y/N]", "✓ added my-server to ~/.ssh/config", "✓ ssh my-server works",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if lines := g.log.Lines(); !slices.Contains(lines, "added my-server to ~/.ssh/config") {
		t.Fatalf("log = %q", lines)
	}

	// No to the lines: the user adds them, and the check waits for Enter.
	l2 := &guideLocal{key: "~/.ssh/id_ed25519.pub", named: map[string]bool{}}
	g2, out2, _ := testGuide(t, context.Background(), guideOpts{}, l2, &clienttest.Transport{}, "203.0.113.10\nagents\n\nvps\nn\n\n", true)
	if h, err := g2.rent(); err != nil || h.Name != "vps" {
		t.Fatalf("%+v %v\n%s", h, err, out2)
	}
	if len(l2.added) != 0 || !strings.Contains(out2.String(), "Add them yourself, then press Enter to check: ") || !slices.Contains(l2.sshCalls, "agents@203.0.113.10") {
		t.Fatalf("added %q, checks %v\n%s", l2.added, l2.sshCalls, out2)
	}
}

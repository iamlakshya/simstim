package onboard

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func ready(kind, title, login string) api.AgentCheck {
	return api.AgentCheck{Kind: kind, Title: title, Installed: true, OnPath: true, Path: "/home/dev/.local/bin/" + kind, Login: login}
}

func kinds(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Agent.Kind)
	}
	return out
}

func TestChecklistOrderAndTicks(t *testing.T) {
	f := NewFlow("box", []api.AgentCheck{ready("codex", "Codex", api.LoginNo), ready("amp", "Amp", api.LoginYes)}, nil)
	got := kinds(f.Items)
	// First-class and supported first, then best effort, each in table order.
	firstBest := slices.IndexFunc(f.Items, Item.BestEffort)
	for i, it := range f.Items {
		if (i >= firstBest) != it.BestEffort() {
			t.Fatalf("best-effort agents are not all at the end: %v", got)
		}
	}
	if got[0] != "claude" || got[1] != "codex" || slices.Contains(got, "shell") {
		t.Fatalf("order = %v", got)
	}
	for _, it := range f.Items {
		if it.Installed() != it.Chosen {
			t.Errorf("%s: installed %v but ticked %v", it.Agent.Kind, it.Installed(), it.Chosen)
		}
	}
	i := slices.Index(got, "codex")
	f.Toggle(i)
	if !f.Items[i].Chosen {
		t.Fatal("an installed agent was unticked")
	}
	if !f.Missing() {
		t.Fatal("Claude Code is missing, but Missing says no")
	}
	if s := f.Items[i].State(); s != "installed, not logged in" {
		t.Fatalf("state = %q", s)
	}
}

func TestChoose(t *testing.T) {
	f := NewFlow("box", nil, nil)
	if err := f.Choose(" codex, shell,none,,claude "); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range f.Chosen() {
		names = append(names, a.Kind)
	}
	if strings.Join(names, ",") != "claude,codex" {
		t.Fatalf("chosen = %v", names)
	}
	err := CheckAgents("claude,nope")
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, `"nope"`) || !strings.Contains(ue.Hint, "agents ls") {
		t.Fatalf("err = %v", err)
	}
	if err := CheckAgents("shell"); err != nil {
		t.Fatalf("shell: %v", err)
	}
}

func TestFlowInstallsThenLogins(t *testing.T) {
	log := &Log{}
	f := NewFlow("box", []api.AgentCheck{ready("claude", "Claude Code", api.LoginYes)}, log)
	if err := f.Choose("codex,opencode"); err != nil {
		t.Fatal(err)
	}
	f.Start()
	a := f.Next()
	if a.Kind != ActInstall || a.Agent.Kind != "codex" || a.N != 1 || a.Of != 2 || a.Args.Name != "install-codex" {
		t.Fatalf("first = %+v", a)
	}
	// Codex installs, but is not logged in.
	a.Session = "install1"
	if err := f.Done(a, []api.AgentCheck{ready("claude", "Claude Code", api.LoginYes), ready("codex", "Codex", api.LoginNo)}, true); err != nil {
		t.Fatal(err)
	}
	b := f.Next()
	if b.Kind != ActInstall || b.Agent.Kind != "opencode" || b.N != 2 {
		t.Fatalf("second = %+v", b)
	}
	// The user leaves opencode's installer running.
	b.Session = "install2"
	if err := f.Done(b, nil, false); err != nil {
		t.Fatal(err)
	}
	if l := f.Line("opencode"); l.Mark != "✕" || !strings.Contains(l.Try, "installer still runs") || !strings.Contains(l.Try, "box/install2") {
		t.Fatalf("opencode line = %+v", l)
	}
	c := f.Next()
	if c.Kind != ActLogin || c.Agent.Kind != "codex" || c.Args.Name != "login-codex" || c.Of != 1 {
		t.Fatalf("login = %+v", c)
	}
	c.Session = "login1"
	if err := f.Done(c, []api.AgentCheck{ready("codex", "Codex", api.LoginYes)}, true); err != nil {
		t.Fatal(err)
	}
	if d := f.Next(); d.Kind != ActDone {
		t.Fatalf("then %+v", d)
	}
	want := []string{"installed Codex on box", "logged in to Codex on box"}
	if got := log.Lines(); !slices.Equal(got, want) {
		t.Fatalf("log = %q, want %q", got, want)
	}
	if got := f.FirstSession(); got != brand.Name+" new box --agent claude" {
		t.Fatalf("first session = %q", got)
	}
	var shown []string
	for _, l := range f.Summary() {
		shown = append(shown, l.Title)
	}
	if !slices.Contains(shown, "opencode") || slices.Contains(shown, "Gemini CLI") {
		t.Fatalf("summary covers %v", shown)
	}
}

func TestFlowSkipStopAndNoLogin(t *testing.T) {
	checks := []api.AgentCheck{ready("codex", "Codex", api.LoginNo), ready("claude", "Claude Code", api.LoginNo)}
	f := NewFlow("box", checks, nil)
	a := f.Next()
	if a.Kind != ActLogin || a.Agent.Kind != "claude" || a.Of != 2 {
		t.Fatalf("first = %+v", a)
	}
	f.Skip(a)
	if b := f.Next(); b.Agent.Kind != "codex" {
		t.Fatalf("after a skip: %+v", b)
	}
	f.Stop()
	if c := f.Next(); c.Kind != ActDone {
		t.Fatalf("after a stop: %+v", c)
	}

	g := NewFlow("box", checks, nil)
	g.NoLogin = true
	if a := g.Next(); a.Kind != ActDone {
		t.Fatalf("NoLogin offered %+v", a)
	}
	if got := g.FirstSession(); got != brand.Name+" new box --agent claude" {
		t.Fatalf("first session = %q", got)
	}
	if got := NewFlow("box", nil, nil).FirstSession(); got != brand.Name+" new box" {
		t.Fatalf("with no agent: %q", got)
	}
}

func TestInstallAndLoginArgs(t *testing.T) {
	claude, _ := agents.Find("claude")
	a := InstallArgs(claude)
	if a.Wall != "none" || !a.Downgrade || a.Name != "install-claude" || a.Cwd != "~" || a.Setup != api.SetupSkip || !slices.Equal(a.Argv, []string{"/bin/sh", "-c", claude.Setup.Install}) {
		t.Fatalf("install args = %+v", a)
	}
	l, ok := LoginArgs(claude)
	if !ok || l.Wall != "none" || !l.Downgrade || !slices.Equal(l.Argv, []string{"/bin/sh", "-c", "claude auth login"}) || l.Agent != "" {
		t.Fatalf("login args = %+v", l)
	}
	agy, _ := agents.Find("antigravity")
	if l, _ := LoginArgs(agy); l.Wall != "none" || !l.Downgrade || l.Agent != "antigravity" || len(l.Argv) != 0 {
		t.Fatalf("an agent whose first start is its login starts as itself: %+v", l)
	}
	aider, _ := agents.Find("aider")
	if _, ok := LoginArgs(aider); ok {
		t.Fatal("aider has no login command")
	}
	// The login command is agents login, also for an agent whose first
	// start is its login (cli-spec 12.3).
	for _, c := range []struct {
		a    agents.Agent
		want string
	}{{claude, brand.Name + " agents login claude box"}, {agy, brand.Name + " agents login antigravity box"}, {aider, ""}} {
		if got := LoginCommand("box", c.a); got != c.want {
			t.Errorf("LoginCommand(box, %s) = %q, want %q", c.a.Kind, got, c.want)
		}
	}
	if got := InstallCommand("box", "codex"); got != brand.Name+" agents install codex box" {
		t.Errorf("InstallCommand = %q", got)
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		c          api.AgentCheck
		mark, want string
	}{
		{api.AgentCheck{Kind: "gemini", Title: "Gemini CLI"}, "✕", "agents install gemini box"},
		{api.AgentCheck{Kind: "opencode", Title: "opencode", Installed: true, Path: "/home/dev/.opencode/bin/opencode"}, "✕", `export PATH="/home/dev/.opencode/bin:$PATH"`},
		{ready("claude", "Claude Code", api.LoginYes), "✓", ""},
		{ready("codex", "Codex", api.LoginNo), "!", "agents login codex box"},
		{ready("antigravity", "Antigravity CLI", api.LoginUnknown), "?", "if it asks you to log in: " + brand.Name + " agents login antigravity box"},
		{ready("aider", "Aider", api.LoginNo), "!", "--secret ANTHROPIC_API_KEY"},
	}
	for _, tc := range cases {
		l := Describe("box", tc.c)
		if l.Mark != tc.mark || !strings.Contains(l.Try, tc.want) || l.Short == "" {
			t.Errorf("%s: %+v, want mark %s and a try with %q", tc.c.Kind, l, tc.mark, tc.want)
		}
	}
	dirty := Describe("box", api.AgentCheck{Kind: "claude\x1b[31m", Title: "Claude\x1b[2J", Installed: true, OnPath: true, Path: "/bin/claude\x1b]0;bad\x07", Login: api.LoginYes, LoginHow: "ok\r\nnow"})
	if strings.ContainsAny(dirty.Title+dirty.State+dirty.Try, "\x1b\x07\r\n") {
		t.Errorf("Describe kept control characters: %+v", dirty)
	}
}

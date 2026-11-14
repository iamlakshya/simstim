package tui

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Harness: runs the model the way Bubble Tea would, with fake time.

type timerMsg struct {
	d   time.Duration
	msg tea.Msg
}

type harness struct {
	t       *testing.T
	m       *Model
	be      *fakeBackend
	clock   time.Time
	results chan tea.Msg
	running atomic.Int64
	timers  []timerMsg
	quit    bool
	raw     []string // what the model wrote to the terminal itself
}

func newHarness(t *testing.T, be *fakeBackend, w, h int) *harness {
	t.Helper()
	hs := &harness{t: t, be: be, clock: now, results: make(chan tea.Msg, 4096)}
	m := New(context.Background(), be, Options{Now: func() time.Time { return hs.clock }})
	m.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		return func() tea.Msg { return timerMsg{d, msg} }
	}
	m.exec = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return fn(c.Run()) }
	}
	m.tierWait = func(wait func() tea.Msg) tea.Msg {
		be.blocked.Add(1)
		defer be.blocked.Add(-1)
		return wait()
	}
	hs.m = m
	t.Cleanup(m.closeAfterProgram)
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.do(m.Init())
	return hs
}

// do runs cmd and everything it leads to, until only goroutines waiting
// for a connection to end are left.
func (hs *harness) do(cmd tea.Cmd) {
	hs.t.Helper()
	hs.start(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case msg := <-hs.results:
			hs.deliver(msg)
			continue
		default:
		}
		if hs.running.Load() <= hs.be.blocked.Load() {
			// Settle once more, in case a waiter just woke.
			time.Sleep(2 * time.Millisecond)
			if len(hs.results) == 0 && hs.running.Load() <= hs.be.blocked.Load() {
				return
			}
			continue
		}
		if time.Now().After(deadline) {
			hs.t.Fatalf("commands did not settle: %d running, %d waiting", hs.running.Load(), hs.be.blocked.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func (hs *harness) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	hs.running.Add(1)
	go func() {
		msg := cmd()
		hs.results <- msg
		hs.running.Add(-1)
	}()
}

func (hs *harness) deliver(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			hs.start(c)
		}
	case timerMsg:
		hs.timers = append(hs.timers, msg)
	case tea.QuitMsg:
		hs.quit = true
	case tea.RawMsg:
		hs.raw = append(hs.raw, fmt.Sprint(msg.Msg))
	default:
		_, cmd := hs.m.Update(msg)
		hs.start(cmd)
	}
}

// send delivers one message and settles.
func (hs *harness) send(msg tea.Msg) {
	hs.t.Helper()
	_, cmd := hs.m.Update(msg)
	hs.do(cmd)
}

// key presses keys by name, in order.
func (hs *harness) key(keys ...string) {
	hs.t.Helper()
	for _, k := range keys {
		hs.send(keyMsg(k))
	}
}

// typeText types each rune as a key press.
func (hs *harness) typeText(s string) {
	hs.t.Helper()
	for _, r := range s {
		hs.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// fire delivers the pending timers whose message matches.
func (hs *harness) fire(match func(tea.Msg) bool) int {
	hs.t.Helper()
	var keep []timerMsg
	var due []timerMsg
	for _, t := range hs.timers {
		if match(t.msg) {
			due = append(due, t)
		} else {
			keep = append(keep, t)
		}
	}
	hs.timers = keep
	for _, t := range due {
		hs.send(t.msg)
	}
	return len(due)
}

func keyMsg(k string) tea.KeyPressMsg {
	special := map[string]tea.KeyPressMsg{
		"enter":      {Code: tea.KeyEnter},
		"ctrl+enter": {Code: tea.KeyEnter, Mod: tea.ModCtrl},
		"esc":        {Code: tea.KeyEscape},
		"tab":        {Code: tea.KeyTab},
		"shift+tab":  {Code: tea.KeyTab, Mod: tea.ModShift},
		"up":         {Code: tea.KeyUp},
		"down":       {Code: tea.KeyDown},
		"left":       {Code: tea.KeyLeft},
		"right":      {Code: tea.KeyRight},
		"backspace":  {Code: tea.KeyBackspace},
		"space":      {Code: tea.KeySpace, Text: " "},
		"ctrl+c":     {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+o":     {Code: 'o', Mod: tea.ModCtrl},
		"ctrl+u":     {Code: 'u', Mod: tea.ModCtrl},
		"ctrl+f":     {Code: 'f', Mod: tea.ModCtrl},
		"alt+e":      {Code: 'e', Mod: tea.ModAlt},
	}
	if m, ok := special[k]; ok {
		return m
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// Fixtures.

func ago(d time.Duration) time.Time { return now.Add(-d) }

func code(n int) *int { return &n }

// labBackend is three hosts: two online with sessions, one unreachable.
func labBackend() *fakeBackend {
	be := newFakeBackend()
	be.hosts = []client.Host{
		{Name: "atlas-dev", SSH: "atlas-dev"},
		{Name: "gcp-dev", SSH: "dev@203.0.113.7", SSHConfig: "~/src/infra/ssh/config"},
		{Name: "aws-gpu", SSH: "aws-gpu"},
	}
	be.state["atlas-dev"] = &fakeHost{
		latency: 9 * time.Millisecond,
		info: client.HostInfo{User: "dev", Distro: "Debian 13", Arch: "amd64", CPUs: 4, MemTotal: 8 << 30, DiskFree: 61 << 30,
			Version: "0.1.0", MemoryMax: 6551502848,
			Agents:        map[string]string{"claude": "/usr/local/bin/claude", "codex": "/usr/local/bin/codex", "opencode": "/usr/bin/opencode", "shell": "/usr/bin/zsh"},
			AgentVersions: map[string]string{"claude": "2.1.3", "codex": "0.46.0", "opencode": "1.0.12"}},
		sessions: []client.Session{
			{ID: "s5", Name: "docs-pass", Agent: "opencode", Cwd: "/home/dev/src/docs", State: api.StateExited, ExitCode: code(0), Since: ago(3 * time.Hour), CreatedAt: ago(5 * time.Hour)},
			{ID: "s2", Name: "notes", Agent: "shell", Cwd: "/home/dev", State: api.StateIdle, Since: ago(time.Hour), CreatedAt: ago(26 * time.Hour), Alive: true},
			{ID: "s3", Name: "api-tests", Agent: "codex", Cwd: "/home/dev/src/api", State: api.StateWorking, Since: ago(14 * time.Second), CreatedAt: ago(40 * time.Minute), Alive: true},
			{ID: "s4", Name: "auth-refactor", Agent: "claude", Cwd: "/home/dev/src/app", State: api.StateNeedsInput, Since: ago(2 * time.Minute), CreatedAt: ago(time.Hour),
				Alive: true, MemoryCurrent: 6549825126, MemoryMax: 8 << 30},
		},
		screens: map[string][]byte{
			"s4": []byte("\x1b[?1049h\x1b[2J\x1b[H\x1b[c" +
				"\x1b[1m✻ Welcome back\x1b[0m\r\n\r\n" +
				"> Refactor the auth module to use the new token store\r\n\r\n" +
				"\x1b[32m●\x1b[0m I'll start by reading the current implementation.\r\n\r\n" +
				"  Read internal/auth/store.go (212 lines)\r\n" +
				"  Read internal/auth/session.go (96 lines)\r\n\r\n" +
				"\x1b[33m●\x1b[0m Should I also migrate the old sessions table? (y/n)\r\n\r\n" +
				"> "),
			"s3": []byte("running 48 tests\r\n\x1b[32mok\x1b[0m  api/handlers  0.41s\r\n"),
		},
	}
	be.state["gcp-dev"] = &fakeHost{
		latency: 41 * time.Millisecond,
		info:    client.HostInfo{Distro: "Ubuntu 24.04", Arch: "amd64", CPUs: 8, MemTotal: 32 << 30, MemoryMax: 30431772672, Agents: map[string]string{"gemini": "/usr/bin/gemini", "shell": "/bin/bash"}},
		sessions: []client.Session{
			{ID: "g6", Name: "bench", Agent: "gemini", Cwd: "/srv/bench", State: api.StateWorking, Since: ago(5 * time.Minute), CreatedAt: ago(2 * time.Hour), Alive: true},
			{ID: "g2", Name: "scratch", Agent: "shell", Cwd: "/tmp", State: api.StateExited, ExitCode: code(1), Since: ago(50 * time.Hour), CreatedAt: ago(60 * time.Hour)},
		},
	}
	be.state["aws-gpu"] = &fakeHost{dialErr: &client.UserError{
		Msg:  "cannot reach aws-gpu: ssh: connect to host 198.51.100.4 port 22: Operation timed out",
		Hint: "ssh aws-gpu   (check that this works first)",
	}}
	return be
}

func uvDraw(c *lipgloss.Canvas, s string) { uv.NewStyledString(s).Draw(c, c.Bounds()) }

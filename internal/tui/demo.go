package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Demo mode shows the launcher with sample hosts and sessions. Nothing
// connects anywhere. It lets people try the screens before they add a host,
// and it is how the screenshots in the README are made.

// RunDemo opens the launcher on sample data. With empty, it shows the
// first-run screen instead.
func RunDemo(ctx context.Context, empty bool) error {
	return runWith(ctx, newDemoBackend(empty), Options{})
}

type demoBackend struct {
	mu       sync.Mutex
	hosts    []client.Host
	sessions map[string][]client.Session
	offline  map[string]bool
}

func newDemoBackend(empty bool) *demoBackend {
	b := &demoBackend{sessions: map[string][]client.Session{}, offline: map[string]bool{"hetzner-box": true}}
	if empty {
		return b
	}
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	b.hosts = []client.Host{
		{Name: "homelab", SSH: "homelab", Labels: map[string]string{"provider": "proxmox"}},
		{Name: "gcp-dev", SSH: "gcp-dev", Labels: map[string]string{"provider": "gcp"}},
		{Name: "hetzner-box", SSH: "hetzner-box"},
	}
	b.sessions["homelab"] = []client.Session{
		{ID: "k2v7m4qa", Name: "auth-refactor", Agent: "claude", Cwd: "/home/me/src/app", State: "needs_input", Source: "screen:permission_prompt",
			Reason: "Do you want to proceed?", CreatedAt: ago(2 * time.Minute), Since: ago(40 * time.Second), Alive: true,
			MemoryCurrent: 612 << 20, MemoryMax: 8 << 30, Cols: 120, Rows: 34},
		{ID: "p9x3hdte", Name: "api-tests", Agent: "codex", Cwd: "/home/me/src/api", State: "working", Source: "screen:esc_to_interrupt",
			CreatedAt: ago(14 * time.Second), Since: ago(5 * time.Second), Alive: true, MemoryCurrent: 140 << 20, MemoryMax: 8 << 30, Cols: 120, Rows: 34},
		{ID: "w5c8n2rb", Name: "docs-site", Agent: "opencode", Cwd: "/home/me/src/docs", State: "idle", Source: "quiet",
			CreatedAt: ago(time.Hour), Since: ago(20 * time.Minute), Alive: true, MemoryCurrent: 380 << 20, MemoryMax: 8 << 30, Cols: 120, Rows: 34},
		{ID: "j6r4y7uf", Name: "notes", Agent: "shell", Cwd: "/home/me", State: "exited", CreatedAt: ago(3 * time.Hour), Since: ago(2 * time.Hour),
			ExitCode: new(int), Alive: true, Cols: 120, Rows: 34},
	}
	b.sessions["gcp-dev"] = []client.Session{
		{ID: "t3f8a1zk", Name: "data-pipeline", Agent: "gemini", Cwd: "/home/me/pipeline", State: "working", Source: "hook",
			CreatedAt: ago(26 * time.Minute), Since: ago(3 * time.Second), Alive: true, MemoryCurrent: 290 << 20, MemoryMax: 6 << 30, Cols: 120, Rows: 34},
	}
	return b
}

func (b *demoBackend) LoadHosts() ([]client.Host, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]client.Host(nil), b.hosts...), nil
}

func (b *demoBackend) SaveHosts(h []client.Host) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hosts = append([]client.Host(nil), h...)
	return nil
}

func (b *demoBackend) Settings() client.Settings {
	s := client.LoadSettings()
	s.Keymap = nil
	return s
}

func (b *demoBackend) HelperVersion() string { return "0.1.0" }

// The demo forgets: it starts the same way every time.
func (b *demoBackend) Memory() memory                           { return memory{} }
func (b *demoBackend) Remember(memory)                          {}
func (b *demoBackend) SetupTrust(string) map[string]bool        { return nil }
func (b *demoBackend) RememberSetup(string, string, bool) error { return nil }
func (b *demoBackend) Secrets(context.Context, client.Host) (map[string]api.Secret, error) {
	return nil, nil
}

func (b *demoBackend) Dial(_ context.Context, h client.Host) (Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.offline[h.Name] {
		return nil, &client.UserError{Msg: "cannot reach " + h.Name + ": connection timed out", Hint: "ssh " + h.SSH}
	}
	return &demoConn{be: b, host: h.Name, done: make(chan struct{})}, nil
}

func (b *demoBackend) Probe(context.Context, client.Host) (client.Platform, error) {
	return client.Platform{OS: "linux", Arch: "amd64"}, nil
}

func (b *demoBackend) Install(context.Context, client.Host, client.Platform) error {
	time.Sleep(600 * time.Millisecond)
	return nil
}

func (b *demoBackend) HelperSize(client.Platform) (int, bool) { return 6_400_000, true }

func (b *demoBackend) SSHConfigHosts() []string {
	return []string{"homelab", "gcp-dev", "hetzner-box", "work-vm", "pi"}
}

// Attach shows a made-up agent screen that fills the whole terminal, the
// way a real session does, until the detach key.
func (b *demoBackend) Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error) {
	b.mu.Lock()
	var s client.Session
	for _, x := range b.sessions[o.Host.Name] {
		if x.ID == o.Session {
			s = x
		}
	}
	b.mu.Unlock()
	in, out := o.In, o.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return client.AttachResult{}, fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(in.Fd()), old) }()
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		return client.AttachResult{}, fmt.Errorf("read the terminal: %w", err)
	}
	defer cr.Close()
	keys := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := cr.Read(buf)
			if n > 0 {
				keys <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	cols, rows := 0, 0
	draw := func() {
		c, r, err := term.GetSize(int(out.Fd()))
		if err != nil || c == cols && r == rows {
			return
		}
		cols, rows = c, r
		_, _ = io.WriteString(out, demoScreen(s, cols, rows))
	}
	_, _ = io.WriteString(out, "\x1b[?1049h")
	draw()
	defer func() {
		cr.Cancel()
		_, _ = io.WriteString(out, "\x1b[?1049l")
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case k, ok := <-keys:
			// Ctrl+\, as a legacy byte or a kitty keyboard sequence.
			if !ok || bytes.IndexByte(k, 0x1c) >= 0 || bytes.Contains(k, []byte("\x1b[92;5u")) {
				return client.AttachResult{Detached: true}, nil
			}
		case <-tick.C:
			draw()
		case <-ctx.Done():
			return client.AttachResult{Detached: true}, nil
		}
	}
}

type demoConn struct {
	be   *demoBackend
	host string
	done chan struct{}
	once sync.Once
}

func (c *demoConn) Sessions(context.Context) ([]client.Session, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	out := append([]client.Session(nil), c.be.sessions[c.host]...)
	now := time.Now()
	for i := range out {
		switch out[i].State {
		case "working":
			out[i].LastActivityAt = now // it keeps writing, so its trace is a wave
		case "needs_input":
			out[i].LastActivityAt = out[i].Since // it went quiet when it asked
		}
	}
	return out, nil
}

func (c *demoConn) NewSession(_ context.Context, a client.NewArgs) (*client.Session, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	name := a.Name
	if name == "" {
		parts := strings.Split(strings.TrimRight(a.Cwd, "/"), "/")
		name = parts[len(parts)-1]
	}
	s := client.Session{ID: "n" + time.Now().Format("150405"), Name: name, Agent: a.Agent, Cwd: a.Cwd, State: "working",
		CreatedAt: time.Now(), Since: time.Now(), Alive: true, Cols: a.Cols, Rows: a.Rows}
	c.be.sessions[c.host] = append(c.be.sessions[c.host], s)
	return &s, nil
}

func (c *demoConn) Delete(_ context.Context, id string) (client.DeleteResult, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	list := c.be.sessions[c.host]
	for i, s := range list {
		if s.ID == id {
			c.be.sessions[c.host] = append(list[:i:i], list[i+1:]...)
			return client.DeleteResult{Deleted: true}, nil
		}
	}
	return client.DeleteResult{}, errors.New("no such session")
}

func (c *demoConn) RenameSession(_ context.Context, id, name string) error {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	for i, s := range c.be.sessions[c.host] {
		if s.ID == id {
			c.be.sessions[c.host][i].Name = name
		}
	}
	return nil
}

func (c *demoConn) Doctor(context.Context) (client.HostInfo, error) {
	return client.HostInfo{Hostname: c.host, User: "me", OS: "linux", Arch: "amd64", Distro: "Debian GNU/Linux 13 (trixie)", CPUs: 4,
		MemTotal: 16 << 30, DiskFree: 61 << 30, Version: "0.1.0", MemoryMax: 14282653696,
		Agents: map[string]string{"claude": "/home/me/.local/bin/claude", "codex": "/home/me/.local/bin/codex",
			"opencode": "/home/me/.opencode/bin/opencode", "gemini": "/usr/local/bin/gemini", "shell": "/bin/bash"}}, nil
}

func (c *demoConn) Peek(_ context.Context, id string, _ int) ([]byte, int, int, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	for _, s := range c.be.sessions[c.host] {
		if s.ID == id {
			return []byte(demoScreen(s, 96, 22)), 96, 22, nil
		}
	}
	return nil, 0, 0, errors.New("no such session")
}

func (c *demoConn) Setup(context.Context) (client.SetupResult, error) {
	return client.SetupResult{}, nil
}

func (c *demoConn) RoundTrip(context.Context) (time.Duration, error) {
	return map[string]time.Duration{"homelab": 4 * time.Millisecond, "gcp-dev": 38 * time.Millisecond}[c.host], nil
}

func (c *demoConn) Folders(_ context.Context, prefix string) (client.FoldersResult, error) {
	r := client.FoldersResult{Recent: []string{"~/src/app", "~/src/api"}, Repos: []string{"~/src/app", "~/src/api", "~/src/docs"}}
	for _, p := range []string{"~/src/api/", "~/src/app/", "~/src/docs/"} {
		if prefix != "" && strings.HasPrefix(p, prefix) {
			r.Matches = append(r.Matches, p)
		}
	}
	return r, nil
}

func (c *demoConn) Done() <-chan struct{} { return c.done }

func (c *demoConn) Close() { c.once.Do(func() { close(c.done) }) }

// demoScreen is a made-up agent's screen, cols by rows, as its own bytes:
// a transcript at the top and a prompt box at the bottom, edge to edge.
func demoScreen(s client.Session, cols, rows int) string {
	const (
		dim   = "\x1b[2m"
		bold  = "\x1b[1m"
		green = "\x1b[32m"
		red   = "\x1b[31m"
		cyan  = "\x1b[36m"
		mag   = "\x1b[35m"
		reset = "\x1b[0m"
	)
	var talk []string
	switch s.State {
	case "needs_input":
		talk = []string{
			bold + "> " + reset + "Move the session store to the new token format, and keep old tokens working.",
			"",
			mag + "● " + reset + "I'll read the current store first.",
			dim + "  Read internal/auth/store.go (212 lines)" + reset,
			dim + "  Read internal/auth/token.go (96 lines)" + reset,
			"",
			mag + "● " + reset + "Plan: add a v2 encoder, read both formats, migrate on write.",
			"",
			mag + "● " + reset + "Edit internal/auth/token.go",
			green + "  + func encodeV2(t Token) ([]byte, error) {" + reset,
			green + "  +     return json.Marshal(tokenV2{ID: t.ID, Exp: t.Exp})" + reset,
			red + "  - return legacyEncode(t)" + reset,
			"",
			bold + "  Run the migration tests?" + reset,
			"  go test ./internal/auth/... -run Migrate",
			"",
			cyan + "  ❯ 1. Yes" + reset,
			"    2. No, and tell the agent what to do differently",
		}
	case "working":
		talk = []string{
			bold + "> " + reset + "Make the API tests pass on the new schema.",
			"",
			dim + "  ran go test ./... (3 failed)" + reset,
			"  fixing handlers/users.go: the nullable email column",
			"",
			cyan + "∴ Working (14s · esc to interrupt)" + reset,
		}
	default:
		talk = []string{dim + "  (idle)" + reset}
	}
	if rows < 6 || cols < 20 {
		return "\x1b[H\x1b[2J" + strings.Join(talk, "\r\n")
	}
	room := rows - 5 // a blank first row, and the prompt box with its status line
	if len(talk) > room {
		talk = talk[len(talk)-room:]
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	at := func(row int, text string) { fmt.Fprintf(&b, "\x1b[%d;1H%s", row, text) }
	for i, l := range talk {
		at(2+i, " "+l)
	}
	line := strings.Repeat("─", cols-2)
	at(rows-3, dim+"╭"+line+"╮"+reset)
	at(rows-2, dim+"│"+reset+" > "+strings.Repeat(" ", cols-5)+dim+"│"+reset)
	at(rows-1, dim+"╰"+line+"╯"+reset)
	where := strings.Replace(s.Cwd, "/home/me", "~", 1) + " · " + s.Agent
	left := " ? for shortcuts"
	gap := max(cols-len(left)-len([]rune(where))-1, 1)
	at(rows, dim+left+strings.Repeat(" ", gap)+where+" "+reset)
	fmt.Fprintf(&b, "\x1b[%d;5H", rows-1)
	return b.String()
}

// demoTraces gives the sample sessions some recent output, so their traces
// show at once: steady bursts for the working ones, and for the one that
// asked a question, the work before it went quiet.
func (b *demoBackend) demoTraces(now time.Time) map[sessKey][]time.Time {
	out := map[sessKey][]time.Time{}
	b.mu.Lock()
	defer b.mu.Unlock()
	for host, list := range b.sessions {
		for _, s := range list {
			var ev []time.Time
			switch s.State {
			case "working":
				for t := 30 * time.Second; t > 0; t -= 2 * time.Second {
					if (t/(6*time.Second))%3 != 2 { // a pause now and then
						ev = append(ev, now.Add(-t))
					}
				}
			case "needs_input":
				for t := 30 * time.Second; t > 16*time.Second; t -= 2 * time.Second {
					ev = append(ev, now.Add(-t))
				}
			}
			out[sessKey{host, s.ID}] = ev
		}
	}
	return out
}

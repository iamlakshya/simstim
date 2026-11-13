//go:build !windows

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

var pickNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func pickSample() []PickRow {
	s := func(id, name, agent, state string, age time.Duration) client.Session {
		return client.Session{ID: id, Name: name, Agent: agent, State: state, Alive: true, CreatedAt: pickNow.Add(-age)}
	}
	return []PickRow{
		{"nuc", s("c3", "tests", "codex", api.StateIdle, 26*time.Hour)},
		{"box", s("a2", "auth-fix", "claude", api.StateWorking, 2*time.Hour)},
		{"box", s("b2", "web", "shell", api.StateIdle, 5*time.Minute)},
		{"box", s("d4", "needs-you", "claude", api.StateNeedsInput, 40*time.Minute)},
	}
}

func pickerKeys(p *picker, keys ...string) {
	for _, k := range keys {
		p.Update(keyMsg(k))
	}
}

// The picker's first frame, at two widths, against golden files.
func TestPickerGolden(t *testing.T) {
	for _, w := range []int{60, 80} {
		p := newPicker("attach", pickSample(), func() time.Time { return pickNow })
		p.width = w
		p.loading = true
		var b strings.Builder
		b.WriteString(plain(p.render()))
		pickerKeys(p, "w", "e")
		p.loading = false
		b.WriteString("== typed we\n" + plain(p.render()))
		pickerKeys(p, "x")
		b.WriteString("== typed wex\n" + plain(p.render()))
		got := b.String()
		for i, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			if cells(l) > w {
				t.Errorf("width %d: line %d is %d cells wide", w, i+1, cells(l))
			}
		}
		path := filepath.Join("testdata", fmt.Sprintf("picker_%d.golden", w))
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run go test ./internal/tui -update to write it)", err)
		}
		if got != string(want) {
			t.Errorf("the picker at %d differs from %s:\n--- got\n%s--- want\n%s", w, path, got, want)
		}
	}
}

func plain(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestPickerKeys(t *testing.T) {
	p := newPicker("attach", pickSample(), func() time.Time { return pickNow })
	// By host, then as the launcher orders sessions: needs-you first.
	var order []string
	for _, r := range p.matches() {
		order = append(order, r.Address())
	}
	if got := strings.Join(order, " "); got != "box/needs-you box/auth-fix box/web nuc/tests" {
		t.Fatalf("order = %s", got)
	}
	pickerKeys(p, "down", "down", "down", "down", "up")
	if r, _ := p.selected(); r.Address() != "box/web" {
		t.Fatalf("selected %s", r.Address())
	}
	pickerKeys(p, "T", "E")
	if r, _ := p.selected(); len(p.matches()) != 1 || r.Address() != "nuc/tests" {
		t.Fatalf("the filter TE matches %v", p.matches())
	}
	pickerKeys(p, "backspace", "backspace", "x", "y", "z")
	if _, ok := p.selected(); ok || !strings.Contains(plain(p.render()), "No session matches.") {
		t.Fatalf("no match:\n%s", plain(p.render()))
	}
	pickerKeys(p, "enter")
	if p.done {
		t.Fatal("enter with no match closed the picker")
	}
	pickerKeys(p, "ctrl+u", "enter")
	if !p.done || p.chosen == nil || p.chosen.Address() != "box/needs-you" {
		t.Fatalf("enter picked %+v", p.chosen)
	}
	if p.View().Content != "" {
		t.Error("the picker still draws after it closed")
	}

	p = newPicker("attach", pickSample(), time.Now)
	pickerKeys(p, "esc")
	if !p.done || p.chosen != nil {
		t.Fatal("esc did not cancel")
	}
}

// The hosts' lists replace the cached rows, and the selection stays on its
// session. Lists with no session close the picker: with ErrNoSessions when
// a host answered, and with the hosts' error when none did.
func TestPickerRefresh(t *testing.T) {
	rows := pickSample()
	p := newPicker("attach", rows, func() time.Time { return pickNow })
	p.loading = true
	pickerKeys(p, "down", "down")
	if r, _ := p.selected(); r.Address() != "box/web" {
		t.Fatalf("selected %s", r.Address())
	}
	if !strings.Contains(plain(p.render()), "checking hosts…") {
		t.Fatal("the picker does not say that it checks the hosts")
	}
	p.Update(pickedRows{rows: rows[1:]})
	if r, _ := p.selected(); r.Address() != "box/web" || len(p.rows) != 3 || p.loading || p.done {
		t.Fatalf("after the refresh: %s selected, %d rows", r.Address(), len(p.rows))
	}

	// An empty cache draws at once, and says that it looks.
	p = newPicker("attach", nil, func() time.Time { return pickNow })
	p.loading, p.hosts = true, []string{"box", "nuc"}
	if got := plain(p.render()); !strings.Contains(got, "Looking for sessions on box and nuc…") {
		t.Fatalf("an empty cache draws:\n%s", got)
	}
	_, cmd := p.Update(pickedRows{})
	if !p.done || !errors.Is(p.failed, ErrNoSessions) || cmd == nil {
		t.Fatalf("hosts with no session: done %v, failed %v", p.done, p.failed)
	}
	reach := errors.New("cannot reach box")
	p = newPicker("attach", nil, time.Now)
	p.Update(pickedRows{err: reach})
	if !p.done || !errors.Is(p.failed, reach) {
		t.Fatalf("no host answered: done %v, failed %v", p.done, p.failed)
	}
	// A refresh that empties a cached list closes it too.
	p = newPicker("attach", rows, time.Now)
	p.Update(pickedRows{})
	if !errors.Is(p.failed, ErrNoSessions) {
		t.Fatalf("an emptied cache: %v", p.failed)
	}
}

// startPick runs Pick on a new terminal in the background.
func startPick(t *testing.T, o PickOptions) (*hosttest.Terminal, chan pickResult) {
	t.Helper()
	term := newPickTerminal(t)
	return term, runPick(term, o)
}

func newPickTerminal(t *testing.T) *hosttest.Terminal {
	t.Helper()
	return hosttest.NewTerminal(t, 70, 20)
}

// runPick runs Pick on term in the background.
func runPick(term *hosttest.Terminal, o PickOptions) chan pickResult {
	return runPickIn(context.Background(), term, o)
}

// runPickIn runs Pick with ctx on term in the background.
func runPickIn(ctx context.Context, term *hosttest.Terminal, o PickOptions) chan pickResult {
	o.In, o.Out = term.TTY, term.TTY
	done := make(chan pickResult, 1)
	go func() {
		r, err := Pick(ctx, o)
		done <- pickResult{r, err}
	}()
	return done
}

// erased waits until the picker's rows are gone from the screen of term,
// and reports whether they went. The last frame is written before Pick
// returns, and the terminal's reader may show it a moment later.
func erased(term *hosttest.Terminal, cols, rows int) (bool, string) {
	var screen string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		lines, alt := hosttest.Screen(term.Output(), cols, rows)
		screen = strings.Join(lines, "\n")
		if !alt && !strings.Contains(screen, "auth-fix") && !strings.Contains(screen, "↑↓") {
			return true, screen
		}
		if time.Now().After(deadline) {
			return false, screen
		}
	}
}

type pickResult struct {
	r   PickRow
	err error
}

func waitPick(t *testing.T, term *hosttest.Terminal, done chan pickResult) pickResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(10 * time.Second):
		t.Fatalf("the picker did not close; the terminal shows %q", term.Output())
		return pickResult{}
	}
}

// Pick runs inline on a terminal: it never takes the alternate screen, and
// it erases its rows when it closes.
func TestPickInline(t *testing.T) {
	for _, c := range []struct {
		keys string
		want string
		err  error
	}{
		{"we\r", "box/web", nil},
		{"\x1b", "", ErrPickCancelled},
	} {
		term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
		term.WaitFor("needs-you")
		for _, k := range c.keys {
			term.Type(string(k))
			time.Sleep(30 * time.Millisecond)
		}
		res := waitPick(t, term, done)
		if !errors.Is(res.err, c.err) || res.err == nil && res.r.Address() != c.want {
			t.Fatalf("%q: picked %s, %v", c.keys, res.r.Address(), res.err)
		}
		if ok, screen := erased(term, 70, 20); !ok {
			t.Errorf("%q: the picker left its rows, or took the alternate screen:\n%s", c.keys, screen)
		}
	}
}

// The refresh runs in a goroutine that Pick owns: Pick stops it, and waits
// for it, before it returns. Its rows reach the picker.
func TestPickOwnsItsRefresh(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	refresh := func(ctx context.Context) ([]PickRow, error) {
		close(started)
		<-ctx.Done()
		time.Sleep(time.Second) // a refresh that takes a while to stop
		close(ended)
		return nil, ctx.Err()
	}
	term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample(), Refresh: refresh})
	term.WaitFor("checking hosts")
	<-started
	term.Type("\r")
	res := waitPick(t, term, done)
	select {
	case <-ended:
	default:
		t.Fatal("Pick returned while its refresh still ran")
	}
	if res.err != nil || res.r.Address() != "box/needs-you" {
		t.Fatalf("picked %s, %v", res.r.Address(), res.err)
	}

	sample := pickSample()
	term, done = startPick(t, PickOptions{Title: "attach", Hosts: []string{"box"},
		Refresh: func(context.Context) ([]PickRow, error) { return sample[1:2], nil }})
	term.WaitFor("auth-fix")
	term.Type("\r")
	if res := waitPick(t, term, done); res.err != nil || res.r.Address() != "box/auth-fix" {
		t.Fatalf("a refreshed pick: %s, %v", res.r.Address(), res.err)
	}
}

// With an empty cache, the picker draws at once, and says that it looks,
// before any host answers (cli-spec 6.3, rule 5).
func TestPickDrawsAtOnce(t *testing.T) {
	answer := make(chan []PickRow)
	refresh := func(ctx context.Context) ([]PickRow, error) {
		select {
		case rows := <-answer:
			return rows, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	term, done := startPick(t, PickOptions{Title: "attach", Hosts: []string{"box", "nuc"}, Refresh: refresh})
	term.WaitFor("Looking for sessions on box and nuc…")
	answer <- pickSample()[2:3]
	term.WaitFor("box/web")
	term.Type("\r")
	if res := waitPick(t, term, done); res.err != nil || res.r.Address() != "box/web" {
		t.Fatalf("picked %s, %v", res.r.Address(), res.err)
	}
}

// A paste goes into the filter, whole, without its line breaks: a line
// break in it never picks a row, and nothing of it is left for the command
// that goes on. Only an Enter of its own picks.
func TestPickPaste(t *testing.T) {
	p := newPicker("attach", pickSample(), time.Now)
	p.Update(tea.PasteMsg{Content: "a\nrest"})
	if string(p.filter) != "arest" || p.done || p.chosen != nil {
		t.Fatalf("a paste of a\\nrest: filter %q, done %v, chosen %v", string(p.filter), p.done, p.chosen)
	}

	term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
	term.WaitFor("needs-you")
	// The paste comes in two parts, a line break in each. Its whole text
	// is the filter: a picker that took its first line break as Enter
	// closes before it shows "auth-fix" as the filter.
	term.Type("\x1b[200~auth\n")
	term.Type("-fix\r\x1b[201~")
	hosttest.WaitFor(t, 5*time.Second, "the paste in the filter", func() bool {
		lines, _ := hosttest.Screen(term.Output(), 70, 20)
		return strings.Contains(strings.Join(lines, "\n"), "❯ auth-fix")
	})
	select {
	case res := <-done:
		t.Fatalf("the paste picked %s, %v", res.r.Address(), res.err)
	default:
	}
	term.Type("\r")
	if res := waitPick(t, term, done); res.err != nil || res.r.Address() != "box/auth-fix" {
		t.Fatalf("enter after the paste: picked %s, %v", res.r.Address(), res.err)
	}
}

// Ctrl+C in a paste whose end never comes cancels the picker.
func TestPickPasteWithCtrlC(t *testing.T) {
	term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
	term.WaitFor("needs-you")
	term.Type("\x1b[200~we\x03")
	if res := waitPick(t, term, done); !errors.Is(res.err, ErrPickCancelled) {
		t.Errorf("ctrl+c in a paste: picked %s, %v", res.r.Address(), res.err)
	}
}

// colour matches a colour in the picker's output: a foreground or a
// background, of 8, 16, 256 or 16 million colours.
var colour = regexp.MustCompile(`\x1b\[(?:[0-9]+;)*(?:3[0-7]|4[0-7]|9[0-7]|10[0-7]|38;[25]|48;[25])[;m]`)

// The picker's colour follows the client's colour rules: NO_COLOR with any
// value but the empty one turns it off, as TERM=dumb does.
func TestPickColourFollowsNoColour(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	for _, c := range []struct {
		noColor string
		colour  bool
	}{
		{"", true},
		{"yes", false},
		{"0", false},
		{"1", false},
	} {
		t.Setenv("NO_COLOR", c.noColor)
		term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
		term.WaitFor("needs-you")
		term.Type("\x1b")
		_ = waitPick(t, term, done)
		if got := colour.Match(term.Output()); got != c.colour {
			t.Errorf("NO_COLOR=%q: colour %v, want %v", c.noColor, got, c.colour)
		}
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if p := pickProfile(nil, os.Getenv); p != colorprofile.Ascii {
		t.Errorf("TERM=dumb: the profile is %v", p)
	}
}

// queries match what a terminal answers: a query that asks for a reply.
var queries = []*regexp.Regexp{
	regexp.MustCompile(`\x1b\[[?>=]?[0-9;]*c`),                    // device attributes
	regexp.MustCompile(`\x1b\[\??[0-9;]*\$p`),                     // mode reports
	regexp.MustCompile(`\x1b\[\?u`),                               // keyboard enhancements
	regexp.MustCompile(`\x1b\[\??[0-9;]*n`),                       // status and cursor reports
	regexp.MustCompile(`\x1b\[>[0-9;]*q`),                         // terminal version
	regexp.MustCompile(`\x1b\[(1[1-9]|2[01])(;[0-9]*)*t`),         // window reports
	regexp.MustCompile(`\x1b\](4;[0-9]+|1[0-9]|52;[a-z0-9]*);\?`), // colours and clipboard
	regexp.MustCompile(`\x1bP[+$]q`),                              // termcap and settings
}

// The picker sends the terminal no query that asks for a reply, so no
// reply can reach the command that goes on: not on a terminal that Bubble
// Tea would ask about its modes and keyboard either. Once Pick returns,
// the test writes a marker of its own through the terminal, and waits for
// it: then every byte that the picker wrote has come, and is checked.
func TestPickSendsNoQuery(t *testing.T) {
	t.Setenv("TERM", "xterm-kitty")
	term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
	term.WaitFor("needs-you")
	term.Type("w")
	term.WaitFor("1 of 4")
	term.Type("\r")
	if res := waitPick(t, term, done); res.err != nil || res.r.Address() != "box/web" {
		t.Fatalf("picked %s, %v", res.r.Address(), res.err)
	}
	marker := fmt.Sprintf("[the picker has ended %d]", time.Now().UnixNano())
	if _, err := term.TTY.WriteString(marker); err != nil {
		t.Fatal(err)
	}
	out, _, _ := bytes.Cut(term.WaitFor(marker), []byte(marker))
	for _, q := range queries {
		if m := q.Find(out); m != nil {
			t.Errorf("the picker sent the query %q", m)
		}
	}
}

// The picker reads no further than the key that picks: keys typed in the
// same write as the enter stay in the terminal, for the command that goes
// on.
func TestPickReadsNoFurther(t *testing.T) {
	term, done := startPick(t, PickOptions{Title: "attach", Rows: pickSample()})
	term.WaitFor("needs-you")
	term.Type("\rls\x1b[A\r")
	if res := waitPick(t, term, done); res.err != nil || res.r.Address() != "box/needs-you" {
		t.Fatalf("picked %s, %v", res.r.Address(), res.err)
	}
	fd := int(term.TTY.Fd())
	old, err := xterm.MakeRaw(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = xterm.Restore(fd, old) }()
	keys, err := keyread.For(term.TTY)
	if err != nil {
		t.Fatal(err)
	}
	want := []keyread.Key{{Kind: keyread.Rune, Rune: 'l'}, {Kind: keyread.Rune, Rune: 's'}, {Kind: keyread.Up}, {Kind: keyread.Enter}}
	for i, w := range want {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		k, err := keys.ReadKey(ctx)
		cancel()
		if err != nil || k != w {
			t.Fatalf("key %d after the pick: %+v, %v; want %+v", i, k, err, w)
		}
	}
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/tui"
)

// Every action in the launcher's palette has either a runnable CLI command in
// Root() (with any flags it names) or a launcher-only reason, never neither and
// never both (docs/contributing/cli-spec.md 15.3).
func TestPaletteAndCLIAgree(t *testing.T) {
	actions := tui.Palette()
	if len(actions) < 20 {
		t.Fatalf("Palette() returned %d actions: registry looks empty", len(actions))
	}
	for _, problem := range checkPaletteActions(Root(), actions) {
		t.Error(problem)
	}
}
func TestThePaletteCheckFindsEachMistake(t *testing.T) {
	root := Root()
	root.AddCommand(&cobra.Command{Use: "hollow", Short: "Not runnable"})
	for _, tc := range []struct {
		name   string
		action tui.PaletteAction
		want   string
	}{
		{"neither", tui.PaletteAction{Name: "session: mystery", Label: "Mystery"}, "has neither"},
		{"both", tui.PaletteAction{Name: "session: mystery", Label: "Mystery", CLI: "ls", LauncherOnly: "it changes the view"}, "has both"},
		{"preview word", tui.PaletteAction{Name: "session: preview", Label: "Peek at the screen", CLI: "show --screen"}, "says preview"},
		{"missing command", tui.PaletteAction{Name: "session: vanish", Label: "Vanish", CLI: "vanish"}, "does not exist"},
		{"missing subcommand", tui.PaletteAction{Name: "host: vanish", Label: "Vanish", CLI: "host vanish"}, "does not exist"},
		{"hidden command", tui.PaletteAction{Name: "session: kill", Label: "Kill", CLI: "kill"}, "is hidden"},
		{"group command", tui.PaletteAction{Name: "host: group", Label: "Group", CLI: "host"}, "only groups subcommands"},
		{"non-runnable command", tui.PaletteAction{Name: "session: hollow", Label: "Hollow", CLI: "hollow"}, "is not runnable"},
		{"missing flag", tui.PaletteAction{Name: "session: peek", Label: "Peek", CLI: "show --nonexistent"}, "has no flag --nonexistent"},
		{"missing short flag", tui.PaletteAction{Name: "session: peek", Label: "Peek", CLI: "show -Z"}, "has no short flag -Z"},
	} {
		problems := checkPaletteActions(root, []tui.PaletteAction{tc.action})
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%s: got %v, want a problem about %q", tc.name, problems, tc.want)
		}
	}
}
func checkPaletteActions(root *cobra.Command, actions []tui.PaletteAction) []string {
	var problems []string
	for _, a := range actions {
		if strings.Contains(strings.ToLower(a.Name), "preview") || strings.Contains(strings.ToLower(a.Label), "preview") {
			problems = append(problems, fmt.Sprintf("%s (%q): still says preview", a.Name, a.Label))
		}
		hasCLI := strings.TrimSpace(a.CLI) != ""
		hasOnly := strings.TrimSpace(a.LauncherOnly) != ""
		switch {
		case !hasCLI && !hasOnly:
			problems = append(problems, fmt.Sprintf("%s: has neither a CLI command nor a launcher-only reason", a.Name))
			continue
		case hasCLI && hasOnly:
			problems = append(problems, fmt.Sprintf("%s: has both CLI command %q and launcher-only reason %q", a.Name, a.CLI, a.LauncherOnly))
			continue
		case hasOnly:
			continue
		}

		var cmdWords, flags []string
		for _, w := range strings.Fields(a.CLI) {
			switch {
			case strings.HasPrefix(w, "-"):
				flags = append(flags, w)
			case len(flags) == 0:
				cmdWords = append(cmdWords, w)
			default:
				problems = append(problems, fmt.Sprintf("%s: unexpected word %q after flags in %q", a.Name, w, a.CLI))
			}
		}
		c := findExactCommand(root, cmdWords)
		if c == nil {
			problems = append(problems, fmt.Sprintf("%s: CLI command %q does not exist in Root()", a.Name, strings.Join(cmdWords, " ")))
			continue
		}
		if c.Hidden {
			problems = append(problems, fmt.Sprintf("%s: CLI command %q is hidden", a.Name, c.CommandPath()))
		}
		if c.Annotations[groupCommand] != "" {
			problems = append(problems, fmt.Sprintf("%s: CLI command %q only groups subcommands", a.Name, c.CommandPath()))
		}
		if !c.Runnable() {
			problems = append(problems, fmt.Sprintf("%s: CLI command %q is not runnable", a.Name, c.CommandPath()))
		}
		for _, fl := range flags {
			if rest, ok := strings.CutPrefix(fl, "--"); ok {
				name, _, _ := strings.Cut(rest, "=")
				if f := flagOf(c, name); f == nil || f.Hidden {
					problems = append(problems, fmt.Sprintf("%s: %s has no flag --%s", a.Name, c.CommandPath(), name))
				}
				continue
			}
			short := strings.TrimPrefix(fl, "-")
			if len(short) != 1 || shortFlagOf(c, short) == nil {
				problems = append(problems, fmt.Sprintf("%s: %s has no short flag %s", a.Name, c.CommandPath(), fl))
			}
		}
	}
	return problems
}
func findExactCommand(root *cobra.Command, words []string) *cobra.Command {
	at := root
	for _, w := range words {
		var next *cobra.Command
		for _, sub := range at.Commands() {
			if sub.Name() == w || sub.HasAlias(w) {
				next = sub
				break
			}
		}
		if next == nil {
			return nil
		}
		at = next
	}
	return at
}
func flagOf(c *cobra.Command, name string) *cobra.Command {
	if f := c.Flags().Lookup(name); f != nil && !f.Hidden {
		return c
	}
	if f := c.InheritedFlags().Lookup(name); f != nil && !f.Hidden {
		return c
	}
	return nil
}
func shortFlagOf(c *cobra.Command, short string) *cobra.Command {
	if f := c.Flags().ShorthandLookup(short); f != nil && !f.Hidden {
		return c
	}
	if f := c.InheritedFlags().ShorthandLookup(short); f != nil && !f.Hidden {
		return c
	}
	return nil
}

// The UX spec's CLI table has one row for every non-hidden command of Root()
// (docs/contributing/cli-spec.md 15.3).
func TestUXSpecListsEveryCommand(t *testing.T) {
	specPath := filepath.Join("..", "..", "docs", "contributing", "ux-spec.md")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	root := Root()
	rows, parseProblems := uxSpecCLICommands(root, string(raw))
	for _, pr := range parseProblems {
		t.Error(pr)
	}
	for _, pr := range missingUXSpecCommands(root, rows) {
		t.Error(pr)
	}

	// Dropping one row must fail the check.
	if len(rows) > 1 {
		dropped := missingUXSpecCommands(root, rows[1:])
		if len(dropped) == 0 {
			t.Fatal("dropping a row from the UX spec table did not fail")
		}
	}
}
func uxSpecCLICommands(root *cobra.Command, md string) ([]string, []string) {
	_, rest, ok := strings.Cut(md, "\n## 6. CLI\n")
	if !ok {
		return nil, []string{"ux-spec.md has no ## 6. CLI section"}
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	var cmds, problems []string
	seen := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "| Command |") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		if strings.HasPrefix(line, "| ---") {
			continue
		}
		cell := line[1:]
		if end := strings.Index(cell, "`"); end >= 0 {
			if closeTick := strings.Index(cell[end+1:], "`"); closeTick >= 0 {
				code := strings.ReplaceAll(cell[end+1:end+1+closeTick], `\|`, `|`)
				fields := strings.Fields(code)
				if len(fields) == 0 || fields[0] != brand.Name {
					problems = append(problems, fmt.Sprintf("CLI table row %q does not start with %s", code, brand.Name))
					continue
				}
				var words []string
				for _, w := range fields[1:] {
					if strings.HasPrefix(w, "[") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "-") || strings.ContainsAny(w, "=|") {
						break
					}
					words = append(words, w)
				}
				c := findExactCommand(root, words)
				if c == nil || c.Hidden {
					problems = append(problems, fmt.Sprintf("CLI table row %q names no visible command", code))
					continue
				}
				path := c.CommandPath()
				if seen[path] {
					problems = append(problems, fmt.Sprintf("CLI table lists %q more than once", path))
					continue
				}
				seen[path] = true
				cmds = append(cmds, path)
				continue
			}
		}
		problems = append(problems, fmt.Sprintf("CLI table row %q has no backticked command", line))
	}
	return cmds, problems
}
func missingUXSpecCommands(root *cobra.Command, have []string) []string {
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.IsAdditionalHelpTopicCommand() {
			return
		}
		if !slices.Contains(have, c.CommandPath()) {
			missing = append(missing, fmt.Sprintf("ux-spec.md ## 6. CLI has no row for %q", c.CommandPath()))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return missing
}
func TestShowScreenAdversarialAndCancel(t *testing.T) {
	sess := client.Session{
		ID: "s1", Name: "hostile", Agent: "shell", State: api.StateWorking, Alive: true, Cols: 80, Rows: 24,
	}
	_, tr := withFake(t, []client.Session{sess})
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := client.SaveHosts(t.Context(), []client.Host{box}); err != nil {
		t.Fatal(err)
	}
	tr.Screens = map[string][]byte{
		"s1": []byte("\x1b[2J\x1b[HA\x1b[2000000000b\r\n" +
			"\x1b[2000000000I\x1b[2000000000S\x1b[H" +
			"\x1b[2000000000@\x1b[2000000000Xsafe-done"),
	}

	// Open the fake connection and warm up vt's tables before timing showScreen
	// directly so disk/Cobra startup jitter under -race is not part of the < 50ms
	// rendering bound.
	_, _ = tui.ScreenLinesContext(context.Background(), []byte("ok"), 80, 24)
	conn, err := client.Dial(context.Background(), box, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var directErr error
	var t0 time.Time
	var elapsed time.Duration
	directOut, _ := captureOutput(t, func() {
		t0 = time.Now()
		directErr = showScreen(context.Background(), found{host: box, conn: conn, s: &sess}, false)
		elapsed = time.Since(t0)
	})
	if directErr != nil {
		t.Fatalf("showScreen hostile: %v", directErr)
	}
	if elapsed >= 50*time.Millisecond {
		t.Errorf("showScreen hostile rendering took %v, want < 50ms", elapsed)
	}
	if !strings.Contains(directOut, "safe-done") {
		t.Fatalf("showScreen hostile output = %q", directOut)
	}

	// Full CLI command end-to-end also succeeds cleanly.
	out, errOut, err := runApart(t, "show", "box/hostile", "--screen")
	if err != nil || errOut != "" || !strings.Contains(out, "safe-done") {
		t.Fatalf("show --screen hostile CLI: %v\nstdout %q\nstderr %q", err, out, errOut)
	}

	// Pre-cancelled context aborts both showScreen and Root().ExecuteContext promptly.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	t0 = time.Now()
	if err := showScreen(cctx, found{host: box, conn: conn, s: &sess}, false); !errors.Is(err, context.Canceled) || time.Since(t0) >= 50*time.Millisecond {
		t.Fatalf("cancelled showScreen = %v in %v, want context.Canceled in < 50ms", err, time.Since(t0))
	}
	if err := runWithContext(t, cctx, "show", "box/hostile", "--screen"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled show --screen CLI = %v, want context.Canceled", err)
	}
}
func runWithContext(t *testing.T, ctx context.Context, args ...string) error {
	t.Helper()
	var err error
	captureOutput(t, func() {
		root := Root() //nolint:contextcheck // Root constructs the command tree; ExecuteContext receives ctx below.
		root.SetArgs(args)
		err = root.ExecuteContext(withLine(ctx, args))
	})
	return err
}

type tuiDriver struct {
	t       *testing.T
	m       *tui.Model
	start   func(tea.Cmd)
	ops     chan func(*tui.Model)
	stop    chan struct{}
	stopped chan struct{}
}

func newTUIDriver(t *testing.T) *tuiDriver {
	t.Helper()
	msgs := make(chan tea.Msg, 256)
	var start func(tea.Cmd)
	start = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		go func() {
			msg := cmd()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					start(sub)
				}
				return
			}
			if msg != nil {
				msgs <- msg
			}
		}()
	}
	m := tui.New(context.Background(), tui.DefaultBackend(nil), tui.Options{
		After: func(time.Duration, tea.Msg) tea.Cmd { return nil },
	})
	d := &tuiDriver{
		t:       t,
		m:       m,
		start:   start,
		ops:     make(chan func(*tui.Model)),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go func() {
		defer close(d.stopped)
		for {
			select {
			case <-d.stop:
				return
			case fn := <-d.ops:
				fn(m)
			case msg := <-msgs:
				_, cmd := m.Update(msg)
				start(cmd)
			}
		}
	}()
	t.Cleanup(d.close)
	d.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	d.do(func(m *tui.Model) { start(m.Init()) })
	return d
}
func (d *tuiDriver) close() {
	select {
	case <-d.stop:
		return
	default:
		close(d.stop)
		<-d.stopped
		d.m.Close()
	}
}
func (d *tuiDriver) do(fn func(*tui.Model)) {
	done := make(chan struct{})
	d.ops <- func(m *tui.Model) {
		fn(m)
		close(done)
	}
	<-done
}
func (d *tuiDriver) send(msg tea.Msg) {
	d.do(func(m *tui.Model) {
		_, cmd := m.Update(msg)
		d.start(cmd)
	})
}
func (d *tuiDriver) reconnectFromPalette() {
	d.t.Helper()
	d.send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	for _, r := range "host reconnect" {
		d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestOldKeymapPreviewCLI(t *testing.T) {
	newTestHome(t)
	cfg := filepath.Join(client.ConfigDir(), "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[keymap]\n\"session: preview\" = \"p\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runApart(t, "config", "keymap")
	if err != nil {
		t.Fatalf("config keymap: %v", err)
	}
	if !strings.Contains(out, "session: peek                    p\n") || strings.Contains(out, "session: preview") {
		t.Errorf("config keymap stdout = %q", out)
	}
	if strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, oldKeymapPreview.Warning()) {
		t.Errorf("config keymap stderr = %q, want %q", errOut, oldKeymapPreview.Warning())
	}

	jsonOut, jsonErr, err := runApart(t, "config", "keymap", "--json")
	if err != nil {
		t.Fatalf("config keymap --json: %v", err)
	}
	checkJSON(t, "config keymap", jsonOut, false)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &arr); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range arr {
		if m["command"] == "session: peek" {
			found = true
			if keys, _ := m["keys"].([]any); len(keys) != 1 || keys[0] != "p" {
				t.Errorf("session: peek keys = %#v", m["keys"])
			}
		}
	}
	if !found {
		t.Fatal("config keymap --json lacks session: peek")
	}
	var warnLine struct {
		V       int
		Warning map[string]string
	}
	if err := json.Unmarshal([]byte(jsonErr), &warnLine); err != nil || warnLine.V != 1 ||
		warnLine.Warning["code"] != "old_name" || warnLine.Warning["until_version"] != "0.4.0" || warnLine.Warning["until_date"] != "2027-11-11" {
		t.Errorf("config keymap --json stderr = %q (%v)", jsonErr, err)
	}

	// Shell completion never prints warnings on stderr.
	_, compErr, _ := runApart(t, cobra.ShellCompRequestCmd, "config", "keymap", "")
	for _, l := range strings.Split(strings.TrimSpace(compErr), "\n") {
		if l != "" && !strings.HasPrefix(l, "Completion ended with directive:") {
			t.Errorf("completion wrote to stderr: %q", compErr)
		}
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// withSetHosts saves a host of each kind that host set meets: a VM, a
// sandbox, a container and a host that a user added. host set takes the
// lock of the host, in the state folder: the test has its own.
func withSetHosts(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ownState(t)
	t.Setenv("NO_COLOR", "1")
	if err := client.SaveHosts(context.Background(), []client.Host{
		{Name: "vm", Machine: &client.Machine{Provider: "lima", ID: "lima-vm", IdleStop: "30m"}},
		{Name: "cloud", Machine: &client.Machine{Provider: "e2b", ID: "sb1"}},
		{Name: "box", Machine: &client.Machine{Provider: "docker", ID: "c1"}},
		{Name: "mine", SSH: "me@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	old := setIdle
	setIdle = func(_ context.Context, h client.Host) (string, error) {
		return "powers off after " + h.Machine.IdleStop, nil
	}
	t.Cleanup(func() { setIdle = old })
}

// Each value of idle on each kind of host: it is saved, takes effect, and
// prints one line of the change (cli-spec 11.1).
func TestHostSetIdle(t *testing.T) {
	withSetHosts(t)
	for _, c := range []struct {
		host, value, saved, said string
	}{
		{"vm", "2h", "2h", "vm: idle 30m → 2h.\n  ✓ idle stop: powers off after 2h\n"},
		{"vm", "NEVER", "never", "vm: idle 2h → never.\n  ✓ idle stop: powers off after never\n"},
		{"cloud", "45m", "45m", "cloud: idle 15m → 45m.\ncloud pauses 45m after the last client goes, while no session works.\n"},
		{"cloud", "never", "never", "cloud: idle 45m → never.\ncloud pauses only when its busy lease runs out, after the last client goes.\n"},
		{"box", "1h", "1h", "box: idle never → 1h.\nSaved, but docker hosts do not stop by themselves.\n"},
	} {
		if said := okSaid(t, "host", "set", c.host, "idle="+c.value); said != c.said {
			t.Errorf("host set %s idle=%s said %q, want %q", c.host, c.value, said, c.said)
		}
		if h, _ := client.FindHost(c.host); h.Machine.IdleStop != c.saved {
			t.Errorf("host set %s idle=%s saved %q", c.host, c.value, h.Machine.IdleStop)
		}
	}
}

// The first acceptance line of C7: `host set box idle=2h` does what
// `host idle box 2h` did. The old form warns once, and changes the same.
func TestHostSetDoesWhatHostIdleDid(t *testing.T) {
	withSetHosts(t)
	newSaid := okSaid(t, "host", "set", "vm", "idle=2h")
	newFile, _ := os.ReadFile(client.ConfigDir() + "/hosts.toml")
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "vm", Machine: &client.Machine{Provider: "lima", ID: "lima-vm", IdleStop: "30m"}},
		{Name: "cloud", Machine: &client.Machine{Provider: "e2b", ID: "sb1"}}, {Name: "box", Machine: &client.Machine{Provider: "docker", ID: "c1"}},
		{Name: "mine", SSH: "me@example.com"}}); err != nil {
		t.Fatal(err)
	}
	oldSaid := okSaid(t, "host", "idle", "vm", "2h")
	oldFile, _ := os.ReadFile(client.ConfigDir() + "/hosts.toml")
	stripGen := func(b []byte) string {
		s := string(b)
		if strings.HasPrefix(s, "# membership-generation:") {
			_, s, _ = strings.Cut(s, "\n")
		}
		return s
	}
	if stripGen(newFile) != stripGen(oldFile) {
		t.Errorf("hosts.toml after host set:\n%s\nafter host idle:\n%s", newFile, oldFile)
	}
	warning, rest, _ := strings.Cut(oldSaid, "\n")
	if rest != newSaid || strings.Count(oldSaid, "Warning:") != 1 ||
		!strings.HasPrefix(warning, `Warning: "host idle <host> <time>" is now "host set <host> idle=<time>".`) {
		t.Errorf("host idle said %q, host set said %q", oldSaid, newSaid)
	}
}

// The usage errors of host set: each fails with exit code 2 before it
// changes anything.
func TestHostSetChecksEveryPairFirst(t *testing.T) {
	withSetHosts(t)
	before, _ := os.ReadFile(client.ConfigDir() + "/hosts.toml")
	for _, c := range []struct {
		args        []string
		msg, try    string
		suggestions []string
		kind        client.Kind
	}{
		{args: []string{"vm"}, msg: "host set needs a setting, such as idle=2h", try: brand.Name + " host show vm", kind: client.KindUsage},
		{args: []string{"vm", "idl=2h"}, msg: `host set has no setting "idl"`, try: brand.Name + " host set vm idle=2h", suggestions: []string{"idle"}, kind: client.KindUsage},
		{args: []string{"vm", "tir=own"}, msg: `host set has no setting "tir"`, try: brand.Name + " host set vm tier=own", suggestions: []string{"tier"}, kind: client.KindUsage},
		{args: []string{"vm", "floor=none"}, msg: `host set has no setting "floor"`, try: brand.Name + " host set --help", kind: client.KindUsage},
		{args: []string{"vm", "tier=friend"}, msg: `"friend" is not a host tier`, try: "Use own, throwaway or shared.", kind: client.KindUsage},
		{args: []string{"vm", "tier=own", "tier=shared"}, msg: "host set got tier twice", try: brand.Name + " host set vm tier=own", kind: client.KindUsage},
		// A key that may mean one given already is the same as a key
		// given twice: the Try: line keeps one pair, and runs as written.
		{args: []string{"vm", "idle=2h", "IDLE=3h"}, msg: "host set got idle twice", try: brand.Name + " host set vm idle=2h", kind: client.KindUsage},
		{args: []string{"vm", "IDLE=3h", "idle=2h"}, msg: "host set got idle twice", try: brand.Name + " host set vm idle=2h", kind: client.KindUsage},
		{args: []string{"vm", "idle"}, msg: `"idle" is not a setting, which has the form key=value, such as idle=2h`, try: brand.Name + " host set --help", kind: client.KindUsage},
		{args: []string{"vm", "idle=soon"}, msg: `"soon" is not an idle time`, kind: client.KindUsage},
		{args: []string{"vm", "idle=30s"}, msg: `"30s" is not an idle time`, kind: client.KindUsage},
		{args: []string{"vm", "idle=2h", "idle=3h"}, msg: "host set got idle twice", try: brand.Name + " host set vm idle=2h", kind: client.KindUsage},
		{args: []string{"mine", "idle=2h"}, msg: "mine was not made by host create, so it has no idle time", kind: client.KindUsage},
		{args: []string{"nowhere", "idle=2h"}, msg: `no host named "nowhere"`, kind: client.KindNotFound},
	} {
		_, err := line(t, append([]string{"host", "set"}, c.args...)...)
		r := describe(err, nil)
		if r.msg != c.msg || r.kind != c.kind || c.try != "" && r.try != c.try || !slices.Equal(r.suggestions, c.suggestions) {
			t.Errorf("host set %s: %q, try %q, %v, kind %v", strings.Join(c.args, " "), r.msg, r.try, r.suggestions, r.kind)
		}
	}
	if after, _ := os.ReadFile(client.ConfigDir() + "/hosts.toml"); string(after) != string(before) {
		t.Errorf("a failed host set changed hosts.toml:\n%s", after)
	}
}

// A failure in the middle: a pair whose change fails says which pairs
// took effect before it, and the Try: line runs the rest again.
func TestHostSetFailsInTheMiddle(t *testing.T) {
	withSetHosts(t)
	setIdle = func(context.Context, client.Host) (string, error) {
		return "", &client.UserError{Msg: "cannot reach vm", Hint: "ssh vm", Code: "unreachable"}
	}
	// A key that lives on the host, after idle, to show the order.
	applied := false
	old := hostSettings
	hostSettings = append(hostSettings[:len(hostSettings):len(hostSettings)], hostSetting{key: "test",
		check: func(_ client.Host, v string) (string, error) { return v, nil },
		now:   func(client.Host) string { return "a" },
		apply: func(context.Context, io.Writer, client.Host, string) error { applied = true; return nil }})
	t.Cleanup(func() { hostSettings = old })

	var said bytes.Buffer
	err := setHost(context.Background(), &said, "vm", []string{"idle=2h", "test=b"})
	var ue *client.UserError
	// The Try: line is the fix of the error itself, and the message says
	// what to run after it.
	if !errors.As(err, &ue) || ue.Msg != "idle=2h failed on vm: cannot reach vm. These took effect: idle=2h in hosts.toml. After the fix, run "+brand.Name+" host set vm idle=2h test=b" ||
		ue.Hint != "ssh vm" || ue.Code != "unreachable" {
		t.Fatalf("err = %+v", err)
	}
	if applied || said.String() != "vm: idle 30m → 2h.\n" {
		t.Errorf("applied %v, said %q", applied, said.String())
	}

	// The host key fails after idle took effect on the host too.
	setIdle = func(context.Context, client.Host) (string, error) { return "powers off after 3h", nil }
	hostSettings[len(hostSettings)-1].apply = func(context.Context, io.Writer, client.Host, string) error { return errors.New("the host said no") }
	said.Reset()
	err = setHost(context.Background(), &said, "vm", []string{"idle=3h", "test=b"})
	if !errors.As(err, &ue) || ue.Msg != "test=b failed on vm: the host said no. These took effect: idle=3h in hosts.toml, idle=3h on vm" ||
		ue.Hint != brand.Name+" host set vm test=b" || ue.Kind != client.KindFailed {
		t.Fatalf("err = %+v", err)
	}
	if said.String() != "vm: idle 2h → 3h.\n  ✓ idle stop: powers off after 3h\n" {
		t.Errorf("said %q", said.String())
	}
}

// The host-setting tests keep their locks in their own state folder: the
// state folder and the home of the run around them stay empty.
func TestHostSetKeepsToItsOwnState(t *testing.T) {
	outer, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", outer)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Run("host set", func(t *testing.T) {
		withSetHosts(t)
		for _, h := range []string{"vm", "cloud", "box", "mine"} {
			_, _, _ = runApart(t, "host", "set", h, "idle=2h")
		}
		if files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "*.lock")); len(files) == 0 {
			t.Error("host set took no lock in the test's own state folder")
		}
	})
	for _, dir := range []string{outer, home} {
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("%s holds %v (%v)", dir, entries, err)
		}
	}
}

// What the helper says of the idle stop reaches the terminal cleaned: no
// reset (ESC c), no write to the clipboard (OSC 52), and no bidirectional
// control, with its lines kept and its length cut.
func TestHostSetCleansWhatTheHelperSaid(t *testing.T) {
	withSetHosts(t)
	setIdle = func(context.Context, client.Host) (string, error) {
		return "powers off\x1bc after\x1b]52;c;aGk=\x07 2h\u202e\nnext line\r\n" + strings.Repeat(strings.Repeat("x", 900)+"\n", 3), nil
	}
	said := okSaid(t, "host", "set", "vm", "idle=2h")
	for _, bad := range []string{"\x1bc", "\x1b]52", "\x07", "\u202e", "\r"} {
		if strings.Contains(said, bad) {
			t.Errorf("host set printed %q: %q", bad, said)
		}
	}
	// What the helper said is cut as a whole to the bound of the
	// launcher's errors: three lines of 900 runes fit in no more than
	// 1000 runes.
	_, block, _ := strings.Cut(said, "  ✓ idle stop: ")
	block = strings.TrimSuffix(block, "\n")
	if !strings.HasPrefix(block, "powers offc after]52;c;aGk= 2h\nnext line\nxxx") || utf8.RuneCountInString(block) != api.BlockMax {
		t.Errorf("host set said %q (%d runes)", said, utf8.RuneCountInString(block))
	}
}

// host set reads the entry again under the lock: a change that came
// between its first read and the lock stays, beside the new idle time.
func TestHostSetReadsTheHostAgainUnderTheLock(t *testing.T) {
	withSetHosts(t)
	old := beforeHostLock
	beforeHostLock = func(string) {
		_ = saveHost(client.Host{Name: "vm", Machine: &client.Machine{Provider: "lima", ID: "lima-vm", IdleStop: "30m"}, Labels: map[string]string{"k": "v"}})
	}
	t.Cleanup(func() { beforeHostLock = old })
	okSaid(t, "host", "set", "vm", "idle=2h")
	if h, _ := client.FindHost("vm"); h.Machine.IdleStop != "2h" || h.Labels["k"] != "v" {
		t.Errorf("the host is %+v, with the machine %+v", h, h.Machine)
	}
}

// A key that lives on the host prints its change once the host took it.
func TestHostSetAKeyOnTheHost(t *testing.T) {
	withSetHosts(t)
	old := hostSettings
	hostSettings = append(hostSettings[:len(hostSettings):len(hostSettings)], hostSetting{key: "test",
		check: func(_ client.Host, v string) (string, error) { return v, nil },
		now:   func(client.Host) string { return "a" },
		apply: func(_ context.Context, w io.Writer, _ client.Host, _ string) error {
			_, err := io.WriteString(w, "  ✓ sent\n")
			return err
		}})
	t.Cleanup(func() { hostSettings = old })
	var said bytes.Buffer
	if err := setHost(context.Background(), &said, "mine", []string{"test=b"}); err != nil || said.String() != "  ✓ sent\nmine: test a → b.\n" {
		t.Errorf("host set mine test=b: %v, said %q", err, said.String())
	}
}

package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/testhome"
)

func TestLocalFixes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the client shares no SSH connections on Windows, so it has no control sockets to fix")
	}
	sshDirs(t)
	if err := SaveHostsContext(context.Background(), []Host{{Name: "kept", SSH: "kept"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(Host{Name: "kept", SSH: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(ConfigDir(), "ssh", "config-user")
	if err := os.WriteFile(unknown, []byte("# personal config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dead := indexedControlFixture(t, Host{Name: "kept", SSH: "kept"}, strings.Repeat("k", 26))
	livePath := filepath.Join(filepath.Dir(dead), "nf-"+strings.Repeat("b", 40))
	live, err := net.Listen("unix", livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	deadControlSocket(t, dead)

	fixes := LocalFixes(false, nil)
	if len(fixes) != 2 || fixes[0].ID != "ssh-configs" || fixes[1].ID != "control-sockets" {
		t.Fatalf("fixes %+v", fixes)
	}
	if !strings.HasSuffix(fixes[0].Command, wrapperPath(Host{Name: "gone"})) || !strings.HasSuffix(fixes[1].Command, dead) {
		t.Fatalf("commands %q %q", fixes[0].Command, fixes[1].Command)
	}
	for _, f := range LocalFixes(true, []string{"ssh-configs", "control-sockets"}) {
		if !f.Done {
			t.Errorf("%s: %+v", f.ID, f)
		}
	}
	for p, want := range map[string]bool{
		wrapperPath(Host{Name: "kept"}): true, wrapperPath(Host{Name: "gone"}): false,
		unknown:  true,
		livePath: true, dead: false,
	} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", p, err == nil, want)
		}
	}
	if left := LocalFixes(false, nil); len(left) != 0 {
		t.Errorf("left to fix: %+v", left)
	}
}

// A cancel stops the local fixes: with ctx ended, ApplyLocalFixes removes
// no file, marks no fix done, and returns ctx's error.
func TestLocalFixesStopAtACancel(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixes, err := ApplyLocalFixes(ctx, []string{"ssh-configs"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 1 || fixes[0].Done {
		t.Errorf("fixes after a cancel: %+v, %v", fixes, err)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone"})); err != nil {
		t.Errorf("a fix ran after the cancel: %v", err)
	}
}

// cancelAfterFirst cancels ctx once a fix has removed its first file.
func cancelAfterFirst(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := RemovedFile
	RemovedFile = func(string) { cancel() }
	t.Cleanup(func() { RemovedFile = old; cancel() })
	return ctx
}

// A cancel after a fix's first file keeps its other files: the fix looks
// at the cancel before each one.
func TestACancelBetweenTwoFilesKeepsTheSecond(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, name := range []string{"gone1", "gone2"} {
		if _, err := writeWrapper(Host{Name: name, SSH: name}); err != nil {
			t.Fatal(err)
		}
	}
	fixes, err := ApplyLocalFixes(cancelAfterFirst(t), []string{"ssh-configs"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 1 || fixes[0].Done {
		t.Errorf("a cancel after the first file: %+v, %v", fixes, err)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone1"})); err == nil {
		t.Error("the first file is still there")
	}
	if want := []string{"removed " + wrapperPath(Host{Name: "gone1"})}; len(fixes) == 1 && !slices.Equal(fixes[0].Changed, want) {
		t.Errorf("the changes of a fix that a cancel stopped: %q, want %q", fixes[0].Changed, want)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone2"})); err != nil {
		t.Errorf("the second file went after the cancel: %v", err)
	}
}

// A cancel after the first of two fixes keeps the second from starting:
// ApplyLocalFixes looks at the cancel before each fix, so the second one
// has neither a result nor an error.
func TestACancelBetweenTwoFixesStartsNoSecond(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the client shares no SSH connections on Windows, so it has no control sockets to fix")
	}
	sshDirs(t)
	kept := Host{Name: "kept", SSH: "kept"}
	if err := SaveHosts(context.Background(), []Host{kept}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	dead := indexedControlFixture(t, kept, strings.Repeat("k", 26))
	deadControlSocket(t, dead)
	fixes, err := ApplyLocalFixes(cancelAfterFirst(t), []string{"ssh-configs", "control-sockets"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 2 {
		t.Fatalf("a cancel between two fixes: %+v, %v", fixes, err)
	}
	if first := fixes[0]; first.ID != "ssh-configs" || !first.Done {
		t.Errorf("the first fix: %+v", first)
	}
	if second := fixes[1]; second.ID != "control-sockets" || second.Done || second.Error != "" {
		t.Errorf("the second fix started after the cancel: %+v", second)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Errorf("the second fix removed its socket after the cancel: %v", err)
	}
}

// doctor --fix removes only the SSH configs that the client generated:
// those of hosts that are gone, and those under an old file name, which it
// counts apart. A file that a user made, such as a hand-made config-sandbox,
// and a file that a saved host's ssh_config names both stay.
func TestDoctorFixRemovesOnlyGeneratedConfigs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(ConfigDir(), "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "config-work")
	box := Host{Name: "box", SSH: "box"}
	if err := SaveHosts(context.Background(), []Host{box, {Name: "sandbox", SSH: "sandbox"}, {Name: "work", SSH: "work", SSHConfig: work}}); err != nil {
		t.Fatal(err)
	}
	generated := func(name string) string {
		return wrapperHeader + `"` + name + `". Do not edit; it is rewritten on each run.` + "\n"
	}
	files := map[string]string{
		filepath.Join(dir, "config-sandbox"): "Host sandbox\n    HostName 203.0.113.2\n",
		work:                                 generated("old") + "Host work\n",
		filepath.Join(dir, "config-box"):     generated("box"),
		wrapperPath(Host{Name: "gone"}):      generated("gone"),
	}
	for p, text := range files {
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeWrapper(box); err != nil {
		t.Fatal(err)
	}
	fixes := LocalFixes(false, nil)
	if len(fixes) != 1 || fixes[0].Problem != "2 generated SSH config(s) that no host uses: 1 for hosts you removed, 1 under an old file name" {
		t.Fatalf("fixes %+v", fixes)
	}
	LocalFixes(true, []string{"ssh-configs"})
	for p, want := range map[string]bool{
		filepath.Join(dir, "config-sandbox"): true, work: true, wrapperPath(box): true,
		filepath.Join(dir, "config-box"): false, wrapperPath(Host{Name: "gone"}): false,
	} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

// The forget of a host also removes the SSH config that an earlier client
// generated for it under its name alone, but never a file that a user made
// under that name.
func TestTheForgetRemovesTheOldGeneratedConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	box, mine := Host{Name: "box", SSH: "box"}, Host{Name: "mine", SSH: "mine"}
	if err := SaveHosts(context.Background(), []Host{box, mine}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ConfigDir(), "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "config-box")
	made := filepath.Join(dir, "config-mine")
	if err := os.WriteFile(old, []byte(wrapperHeader+`"box". Do not edit; it is rewritten on each run.`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(made, []byte("Host mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, h := range []Host{box, mine} {
		if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &h}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the old generated config of box stays")
	}
	if _, err := os.Stat(made); err != nil {
		t.Errorf("the forget removed a file that a user made: %v", err)
	}
}

func TestLocalChecks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = os.MkdirAll(ConfigDir(), 0o700)
	conf := "[ui]\nmotoin = \"off\"\n\n[secrets]\nA_KEY = \"no-such-program-here read x\"\nB_KEY = \"sh -c 'echo b'\"\n"
	if err := os.WriteFile(settingsPath(), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]Check{}
	for _, c := range LocalChecks() {
		got[c.Name] = c
	}
	if c := got["config.toml"]; c.OK || !strings.Contains(c.Detail, "ui.motoin") || c.Fix != brand.Name+" config check" {
		t.Errorf("config.toml: %+v", c)
	}
	if c := got["hosts.toml"]; !c.OK {
		t.Errorf("hosts.toml: %+v", c)
	}
	if c := got["secret A_KEY"]; c.OK || !strings.Contains(c.Detail, "no-such-program-here is not on PATH") {
		t.Errorf("A_KEY: %+v", c)
	}
	if c := got["secret B_KEY"]; !c.OK {
		t.Errorf("B_KEY: %+v", c)
	}
	if err := os.WriteFile(hostsPath(), []byte("[[host]]\nname =\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := fileCheck("hosts.toml", hostsPath(), &hostsFile{}); c.OK || c.Fix != brand.Name+" config check" {
		t.Errorf("broken hosts.toml: %+v", c)
	}
}

// A config.toml that does not decode gives the defaults alone, and one
// warning with its line. An unknown key decodes, and gives none.
func TestBrokenSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = os.MkdirAll(ConfigDir(), 0o700)
	if w := SettingsWarning(); w != "" {
		t.Errorf("no config.toml warns: %q", w)
	}
	// Every table but [attach] is good. The decoder visits the tables in no
	// fixed order, so each load could keep some of them: none may stay.
	good := "[ui]\nmotion = \"off\"\n[agents]\ninline = true\n[sessions]\nmemory_max = \"4G\"\n[keymap]\n\"session: delete\" = \"x\"\n[secrets]\nA = \"x\"\n"
	for body, want := range map[string]string{
		good + "[attach]\ndetach_key = \"ctrl+]\"\nscrollback = \n":     "config.toml has an error on line 13, so its settings are not used",
		good + "[attach]\ndetach_key = \"ctrl+]\"\nscrollback = true\n": "config.toml has an error on line 13, so its settings are not used",
		good + "[attach]\nmotoin = \"off\"\n":                           "",
	} {
		if err := os.WriteFile(settingsPath(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if w := SettingsWarning(); w != want {
			t.Errorf("%q warns %q, want %q", body, w, want)
		}
		for range 20 {
			s := LoadSettings()
			expected := DefaultSettings()
			expected.loadErr = s.loadErr
			if (want != "") != (s.loadErr != nil) {
				t.Fatal("warning/default recovery lost its exact read-error barrier")
			}
			if broken := want != ""; broken != reflect.DeepEqual(s, expected) {
				t.Fatalf("%q gives %+v", body, s)
			}
		}
	}
	// A config.toml that cannot be read warns too.
	if err := os.Remove(settingsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settingsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if w := SettingsWarning(); w != "config.toml cannot be read, so its settings are not used" {
		t.Errorf("an unreadable config.toml warns %q", w)
	}
	s := LoadSettings()
	expected := DefaultSettings()
	expected.loadErr = s.loadErr
	if !reflect.DeepEqual(s, expected) {
		t.Errorf("an unreadable config.toml gives %+v", s)
	}
	if s.loadErr == nil || ValidateSettings(s) == nil {
		t.Fatal("unreadable defaults became successful grant settings")
	}
}

// The idle time of a machine, by the rule of `host idle`: trimmed, never in
// any case, or a duration of a minute or more.
func TestCheckIdleTime(t *testing.T) {
	for s, ok := range map[string]bool{"never": true, "1m": true, "60s": true, "2h": true, "59s": false, "0s": false, "-5m": false, "5x": false, "": false, "Never": true, " NEVER ": true, " 30m ": true, "never ever": false} {
		if err := CheckIdleTime(s); (err == nil) != ok {
			t.Errorf("CheckIdleTime(%q) = %v", s, err)
		}
	}
	var ue *UserError
	if err := CheckIdleTime("30s"); !errors.As(err, &ue) || ue.Kind != KindUsage || ue.Hint == "" {
		t.Errorf("CheckIdleTime(30s) = %#v", err)
	}
}

// shortDir is a folder with a path short enough for sockets on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := testhome.ShortTempDir("fx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// indexedControlFixture uses the same private marker/epoch publication as
// SSH commands. A basename alone never makes a doctor fixture owned.
func indexedControlFixture(t *testing.T, h Host, key string) string {
	t.Helper()
	base := controlFallbackDir(uint32(os.Getuid()))
	unlock, err := lockPin(context.Background(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	epoch := markControl(base, h.Name, key)
	unlock()
	if epoch == "" {
		t.Fatal("the canonical index did not publish the owned epoch")
	}
	return filepath.Join(epoch, "nf-"+strings.Repeat("a", 40))
}

func deadControlSocket(t *testing.T, path string) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

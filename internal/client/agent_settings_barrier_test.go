//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// Test-only injected owned leases. Production captures the actual mode owner
// and never chooses a path/provider fallback or a fabricated Settings value.
type fixtureAgentSettingsMode struct {
	ctx            context.Context
	root           string
	info           os.FileInfo
	turn           chan struct{}
	workspace      [tomlinspect.IndexWorkspaceBytes]byte
	opened, closed int
}
type fixtureAgentIndex struct {
	mode   *fixtureAgentSettingsMode
	ctx    context.Context
	file   *os.File
	closed bool
}
type fixtureAgentIndexFile struct{ lease *fixtureAgentIndex }

func newFixtureAgentSettingsMode(t *testing.T) *fixtureAgentSettingsMode {
	t.Helper()
	root := t.TempDir()
	for _, entry := range []struct{ name, leaf string }{
		{"HOME", "home"}, {"USERPROFILE", "profile"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_RUNTIME_DIR", "run"}, {"XDG_CONFIG_DIRS", "config-dirs"}, {"XDG_DATA_DIRS", "data-dirs"},
	} {
		path := filepath.Join(root, entry.leaf)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(entry.name, path)
	}
	dir := filepath.Join(root, "owned-mode")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureAgentSettingsMode{ctx: t.Context(), root: dir, info: info, turn: make(chan struct{}, 1)}
}
func (m *fixtureAgentSettingsMode) checkAgentSettingsMode(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(m.root)
	if err != nil || !os.SameFile(info, m.info) || !info.IsDir() || info.Mode().Perm() != 0700 {
		return validatedConfigScopeError()
	}
	return nil
}
func (m *fixtureAgentSettingsMode) AcquireIndex(ctx context.Context, size int64) (tomlinspect.IndexLease, error) {
	select {
	case m.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	keep := false
	defer func() {
		if !keep {
			<-m.turn
		}
	}()
	if err := m.checkAgentSettingsMode(ctx); err != nil {
		return nil, err
	}
	path := filepath.Join(m.root, "index")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, validatedConfigScopeError()
	}
	if err := os.Remove(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	keep = true
	m.opened++
	return &fixtureAgentIndex{mode: m, ctx: ctx, file: file}, nil
}
func (l *fixtureAgentIndex) File() tomlinspect.IndexFile { return fixtureAgentIndexFile{lease: l} }
func (l *fixtureAgentIndex) Workspace() []byte           { return l.mode.workspace[:] }
func (l *fixtureAgentIndex) Close() error {
	if l.closed {
		return nil
	}
	err := l.mode.checkAgentSettingsMode(l.ctx)
	l.closed = true
	err = errors.Join(err, l.file.Close())
	clear(l.mode.workspace[:])
	l.mode.closed++
	<-l.mode.turn
	return err
}
func (f fixtureAgentIndexFile) check() error {
	if f.lease.closed {
		return validatedConfigScopeError()
	}
	return f.lease.mode.checkAgentSettingsMode(f.lease.ctx)
}
func (f fixtureAgentIndexFile) ReadAt(data []byte, offset int64) (int, error) {
	if err := f.check(); err != nil {
		return 0, err
	}
	return f.lease.file.ReadAt(data, offset)
}
func (f fixtureAgentIndexFile) WriteAt(data []byte, offset int64) (int, error) {
	if err := f.check(); err != nil {
		return 0, err
	}
	return f.lease.file.WriteAt(data, offset)
}
func (f fixtureAgentIndexFile) Stat() (os.FileInfo, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.lease.file.Stat()
}

func writeAgentSettingsFixture(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClosedAgentBarrierMatchesActualCanonicalSettings(t *testing.T) {
	mode := newFixtureAgentSettingsMode(t)
	inspector, err := newAgentSettingsInspector(mode)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	for _, input := range []string{
		"", "[ui]\nmotion='auto'\n", "ui={motion.extra=1,motion='auto',\"\"=[]}\n",
		"secrets=1\n", "secrets=[]\n", "[[secrets]]\nrun='unused'\n", "keymap=true\n",
		"[ui]\nmotion=1\n", "[UI]\nMotion='auto'\nmotion='off'\n",
		"[profiles.one.network]\nmodel_budget='wrong'\n", "[[network.rules]]\ntarget=1\n[[network.rules]]\ntarget='example.test'\n",
		fmt.Sprintf("[secrets]\nUNUSED=%q\n", "touch "+ShellQuote(marker)), "[secrets.UNUSED]\nrun=''\n",
		"[secrets.UNUSED]\nrun='unused'\nkind='wrong'\n", "[secrets.UNUSED]\nrun='unused'\non_hosts=[]\n",
		"[secrets.UNUSED]\nrun='unused'\non_hosts=['ends.']\n", "[secrets.UNUSED]\nrun='unused'\nformat=''\n",
		"[secrets.UNUSED]\nrun='unused'\nhosts=['example.test']\nheader='Authorization'\n",
		"[secrets.UNUSED]\nrun='unused'\nhosts=['example.test']\n", "[secrets.UNUSED]\nrun='unused'\nhosts=[]\n",
		"[secrets.UNUSED]\nrun='unused'\nhosts=['example.test']\nheader='Authorization'\nkind='ssh-key'\n",
		"[secrets.UNUSED]\nrun='unused'\nRun='alias'\n", "[secrets.UNUSED]\nrun='unused'\nunknown='forbidden'\n",
		"[secrets]\nSSH_AUTH_SOCK='unused'\n", "[secret_tiers]\nSSH_AUTH_SOCK=' Own '\n",
		"[secret_tiers]\nUNUSED=' Shared '\n", "[secret_tiers]\nUNUSED='invalid'\n",
		"['ſecrets'.UNUSED]\nrun=''\n", "[secrets.UNUSED]\nrun='unused'\n'Kind'='invalid'\n",
		"[secrets]\nSSH_AUTH_SOCK='unused'\n'!bad'='unused'\n",
		"[secrets]\n'~bad'='unused'\nSSH_AUTH_SOCK='unused'\n",
	} {
		writeAgentSettingsFixture(t, input)
		// This is the ACTUAL ordinary reader/normaliser and Allow table,
		// used only as an oracle. No test Settings stand-in is constructed.
		settings, _ := ReadSettings()
		expected := Allow(Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent}, settings)
		actual := inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent})
		if (actual == nil) != (expected == nil) {
			t.Fatalf("CLOSED/actual canonical result differs at input length %d: expected %v actual %v", len(input), expected, actual)
		}
		var wantUser, gotUser *UserError
		if errors.As(expected, &wantUser) && errors.As(actual, &gotUser) && wantUser.Code != gotUser.Code {
			t.Fatalf("canonical error priority differs: expected %s got %s", wantUser.Code, gotUser.Code)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("settings validation executed a command")
		}
		if mode.opened != mode.closed {
			t.Fatal("closed barrier retained its owned index")
		}
	}
}

func TestClosedAgentBarrierRefusesDanglingSettingsLink(t *testing.T) {
	mode := newFixtureAgentSettingsMode(t)
	inspector, err := newAgentSettingsInspector(mode)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), settingsPath()); err != nil {
		t.Fatal(err)
	}
	err = inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "config_read" {
		t.Fatalf("broken symbolic link became defaults or wrong read error: %v", err)
	}
	if mode.opened != mode.closed {
		t.Fatal("broken source retained index resources")
	}
}

func TestClosedAgentBarrierUsesOneActualAllowScope(t *testing.T) {
	mode := newFixtureAgentSettingsMode(t)
	inspector, err := newAgentSettingsInspector(mode)
	if err != nil {
		t.Fatal(err)
	}
	writeAgentSettingsFixture(t, "")
	for _, tier := range []Tier{TierUnknown, TierShared, TierThrowaway, TierOwn} {
		settings, _ := ReadSettings()
		want := Allow(Host{Name: "saved"}, tier, Outbound{Kind: OutboundSSHAgent}, settings)
		got := inspector.allow(t.Context(), Host{Name: "saved"}, tier, Outbound{Kind: OutboundSSHAgent})
		if (want == nil) != (got == nil) {
			t.Fatal("agent tier row diverged")
		}
	}
	for _, kind := range []OutboundKind{OutboundSecret, OutboundSSHKey, OutboundRepo, OutboundPaste, OutboundAlertToken, OutboundPool} {
		if err := inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: kind}); err == nil {
			t.Fatal("agent-only seal approved a different outbound kind")
		}
	}
	if err := inspector.allow(t.Context(), Host{Name: "ends."}, TierOwn, Outbound{Kind: OutboundSSHAgent}); err == nil {
		t.Fatal("common actual host-name check was bypassed")
	}
	if err := inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent, Name: "named"}); err == nil {
		t.Fatal("actual nameless agent rule was bypassed")
	}
	if err := (*agentSettingsBarrier)(nil).check(); err == nil {
		t.Fatal("absent seal passed")
	}
	if err := (&agentSettingsBarrier{}).check(); err == nil {
		t.Fatal("zero seal passed")
	}
}

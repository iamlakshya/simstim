package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func sshEnvDirs(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, t.TempDir())
	}
	if runtime.GOOS != "windows" {
		t.Setenv("XDG_CACHE_HOME", shortDir(t))
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_AGENT_PID", "")
}

func TestSSHEnvironmentRestrictionNeedsMatchingSavedOwnHost(t *testing.T) {
	sshEnvDirs(t)
	h := Host{Name: "box", SSH: "user@box.example.invalid"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		host Host
		tier Tier
		want bool
	}{
		{"own", h, TierOwn, false},
		{"throwaway", h, TierThrowaway, true},
		{"shared", h, TierShared, true},
		{"unknown", h, TierUnknown, true},
		{"unsaved", Host{Name: "other", SSH: h.SSH}, TierOwn, true},
		{"changed target", Host{Name: h.Name, SSH: "user@other.example.invalid"}, TierOwn, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]TierEntry{}
			if tc.tier.Valid() {
				entries[h.Name] = TierEntry{Name: h.Name, Tier: tc.tier}
			}
			if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: entries}); err != nil {
				t.Fatal(err)
			}
			got, err := shouldRestrictSSHEnv(t.Context(), tc.host)
			if err != nil || got != tc.want {
				t.Fatalf("restriction = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
	if err := os.WriteFile(tiersPath(), []byte("host = [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := shouldRestrictSSHEnv(t.Context(), h); !got || err == nil {
		t.Fatalf("unreadable tiers allowed a connection: %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := shouldRestrictSSHEnv(ctx, h); !got || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lookup = %v, %v", got, err)
	}
	if err := os.Remove(tiersPath()); err != nil {
		t.Fatal(err)
	}
	if got, err := shouldRestrictSSHEnv(t.Context(), h); !got || err != nil {
		t.Fatalf("an unrecorded tier was not restricted: %v, %v", got, err)
	}
	if _, err := os.Stat(tiersPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("argument assembly started tier migration: %v", err)
	}
}

func TestSSHEnvironmentCancellationStopsBeforeWritingConfig(t *testing.T) {
	sshEnvDirs(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h := Host{Name: "box", SSH: "box.invalid"}
	before := sshGRuns.Load()
	for _, write := range []func() (string, error){
		func() (string, error) { return writeWrapperContext(ctx, h) },
		func() (string, error) { return DefaultSSHConfigContext(ctx) },
	} {
		if _, err := write(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled config write = %v", err)
		}
	}
	if sshGRuns.Load() != before {
		t.Fatal("a cancelled command queried SSH")
	}
	files, err := filepath.Glob(filepath.Join(ConfigDir(), "ssh", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("a cancelled command wrote SSH files: %v, %v", files, err)
	}
}

func TestSSHOptionCacheContentionHonoursCancellation(t *testing.T) {
	sshEnvDirs(t)
	sshEnvFake(t, "hostname probe.invalid\n", false)
	knowsMu.Lock()
	defer knowsMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	started := make(chan struct{})
	before := sshGRuns.Load()
	go func() {
		close(started)
		_, err := knowsOptionContext(ctx, "TestCancellationOption=no")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled cache wait = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSH option cache wait ignored cancellation")
	}
	if sshGRuns.Load() != before {
		t.Fatal("a cancelled cache waiter queried SSH")
	}
}

func TestSSHSetEnvRefusalNeverIncludesTheValue(t *testing.T) {
	for _, output := range []string{"setenv TOKEN=private-value\n", "SETENV TOKEN=private-value\r\n", "setenv\tTOKEN=private-value\r\n", "setenv \r\n"} {
		vals, err := parseConfigValues([]byte(output))
		if err != nil {
			t.Fatal(err)
		}
		err = refuseSSHSetEnv("box.invalid", vals)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "ssh_setenv" || ue.Kind != KindRefused || ue.Hint == "" {
			t.Fatalf("SetEnv was not refused: %v", err)
		}
		if strings.Contains(err.Error(), "TOKEN") || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("the refusal includes an environment value: %v", err)
		}
	}
	if err := refuseSSHSetEnv("box.invalid", map[string][]string{"sendenv": {"LANG LC_*"}}); err != nil {
		t.Fatalf("SendEnv was refused: %v", err)
	}
}

func TestSSHEnvironmentClearingFollowsEveryInclude(t *testing.T) {
	sshEnvDirs(t)
	h := Host{Name: "box", SSH: "user@box.example.invalid"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	userConfig := filepath.Join(home(), ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("Host *\n    SendEnv PRIVATE_TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	systemPath := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(systemPath, []byte("Host *\n    SendEnv SYSTEM_TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldSystem, oldFork := systemConfig, sshKnowsFork
	systemConfig = func() string { return systemPath }
	sshKnowsFork = func(context.Context) bool { return false }
	t.Cleanup(func() { systemConfig, sshKnowsFork = oldSystem, oldFork })
	const cleared = "    SendEnv -*\n    SendEnv LANG LC_*\n"
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
			h.Name: {Name: h.Name, Tier: tier},
		}}); err != nil {
			t.Fatal(err)
		}
		path, err := writeWrapperContext(t.Context(), h)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if tier == TierOwn {
			if strings.Contains(s, cleared) {
				t.Fatal("a saved own host lost the user's environment settings")
			}
			continue
		}
		if !strings.HasSuffix(s, cleared) || strings.LastIndex(s, "Include ") > strings.Index(s, cleared) {
			t.Fatalf("environment clearing does not follow all Includes:\n%s", s)
		}
		if strings.LastIndex(s, "Host *\n") < strings.LastIndex(s, "Include ") {
			t.Fatalf("environment clearing has no final Host * block:\n%s", s)
		}
	}
	path, err := DefaultSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(b), "Host *\n"+cleared) {
		t.Fatalf("an unsaved target has no final clearing block: %s, %v", b, err)
	}
}

func TestRestrictedSSHWrapperSurvivesAnEarlierOwnWriter(t *testing.T) {
	sshEnvDirs(t)
	h := Host{Name: "box", SSH: "user@box.example.invalid"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	saveTier := func(tier Tier) {
		t.Helper()
		if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
			h.Name: {Name: h.Name, Tier: tier},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	saveTier(TierOwn)
	type ownWriter struct{}
	started, allow, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(allow) })
	oldFork := sshKnowsFork
	sshKnowsFork = func(ctx context.Context) bool {
		if ctx.Value(ownWriter{}) != nil {
			close(started)
			<-allow
		}
		return false
	}
	t.Cleanup(func() {
		release()
		<-finished
		sshKnowsFork = oldFork
	})
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer close(finished)
		path, err := writeWrapperContext(context.WithValue(t.Context(), ownWriter{}, true), h)
		done <- result{path, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the own writer did not reach config publication")
	}
	saveTier(TierShared)
	restricted, err := writeWrapperContext(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	release()
	older := <-done
	if older.err != nil || older.path == restricted {
		t.Fatalf("own and restricted writers share a config: %s, %s, %v", older.path, restricted, older.err)
	}
	b, err := os.ReadFile(restricted)
	if err != nil || !strings.HasSuffix(string(b), "    SendEnv -*\n    SendEnv LANG LC_*\n") {
		t.Fatalf("an older own writer removed environment clearing: %s, %v", b, err)
	}
	if fixes := sshConfigFix(); len(fixes) != 0 {
		t.Fatalf("prune would remove a saved host's active wrapper: %+v", fixes)
	}
	ctx, unlockHost, err := LockHost(t.Context(), h.Name, "wrapper cleanup test")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockHost()
	unlockPin, err := lockPin(ctx, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockPin()
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockHosts()
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := ApplyHostChange(hosts, HostChange{Kind: "host rm", Was: &h})
	if err != nil {
		t.Fatal(err)
	}
	if err := removeHostWrappers(ctx, h.Name, remaining); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{restricted, older.path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("forget left a generated environment config: %s, %v", path, err)
		}
	}
}

// sshEnvFake answers only local configuration queries. Any attempted login
// records a marker, so a refusal cannot silently proceed to a connection.
func sshEnvFake(t *testing.T, output string, failure bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "settings")
	marker := filepath.Join(dir, "connected")
	if err := os.WriteFile(data, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	status := "0"
	if failure {
		status = "1"
	}
	script := "#!/bin/sh\nfor a; do\nif [ \"$a\" = -G ]; then cat " + ShellWord(data) + "; exit " + status + "; fi\ndone\ntouch " + ShellWord(marker) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func TestCheckSSHEnvironmentReadsEveryLineOrRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, output, code string
		failure            bool
	}{
		{"safe", "hostname box.invalid\nsendenv LANG LC_*\n", "", false},
		{"setenv", "hostname box.invalid\nsetenv TOKEN=private-value\r\n", "ssh_setenv", false},
		{"long safe line", "xauthlocation /" + strings.Repeat("x", 70000) + "\nsetenv TOKEN=private-value\n", "ssh_setenv", false},
		{"truncated", "hostname box.invalid\nxauthlocation /" + strings.Repeat("x", maxConfigLine+10) + "\nsetenv TOKEN=private-value\n", "ssh_config_read", false},
		{"empty", "", "ssh_config_read", false},
		{"failed", "setenv TOKEN=private-value\n", "ssh_config_read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sshEnvDirs(t)
			marker := sshEnvFake(t, tc.output, tc.failure)
			err := CheckSSHEnvironment(t.Context(), "box.invalid", []string{"-F", os.DevNull})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var ue *UserError
				if !errors.As(err, &ue) || ue.Code != tc.code || ue.Hint == "" {
					t.Fatalf("configuration check = %v, want %s", err, tc.code)
				}
				if strings.Contains(err.Error(), "private-value") {
					t.Fatalf("configuration failure includes a value: %v", err)
				}
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("configuration check attempted a login: %v", err)
			}
		})
	}
}

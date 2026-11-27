//go:build linux

package guest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func ownedStoreDirectories(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := agentProcess(); err != nil {
		t.Skip("the production owned runner requires root or the fixed agent account")
	}
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Fatal("the Linux runner fixture requires python3")
	}
	root, err := os.MkdirTemp("/tmp", "secret-store-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, relative := range []string{"", "home", "work", "config", "config-dirs", "state", "data", "data-dirs", "cache", "run", "gnupg", "tmp"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(path, AgentUID, AgentGID); err != nil {
				t.Fatal(err)
			}
		}
	}
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"),
		"USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "run"),
		"XDG_CONFIG_DIRS=" + filepath.Join(root, "config-dirs"), "XDG_DATA_DIRS=" + filepath.Join(root, "data-dirs"),
		"GNUPGHOME=" + filepath.Join(root, "gnupg"), "GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1", "TMPDIR=" + filepath.Join(root, "tmp"),
		"SSL_CERT_FILE=" + filepath.Join(root, "ca.pem"), "HTTP_PROXY=http://127.0.0.1:3128",
	}
	return root, env
}

func waitForOwnedStore(t *testing.T, guest *Init, ready string) *execRun {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(ready); err == nil {
			guest.mu.Lock()
			for run := range guest.owned {
				guest.mu.Unlock()
				return run
			}
			guest.mu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the owned Store command did not reach its fixture barrier")
	return nil
}

func awaitOwnedStore(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Store did not return after owned cleanup")
		return nil
	}
}

func TestOwnedStoreReceivesOnlySentinelsWithActualAgentIdentityAndEnvironment(t *testing.T) {
	root, base := ownedStoreDirectories(t)
	report := filepath.Join(root, "work", "report.json")
	const literal = "literal;$VALUE"
	const script = `import json, os, sys
value = sys.stdin.read()
report = {"input": value, "argv": sys.argv[1:], "uid": os.geteuid(), "gid": os.getegid(), "groups": os.getgroups(), "cwd": os.getcwd(), "home": os.environ.get("HOME"), "key": os.environ.get("STORE_KEY"), "ca": os.environ.get("SSL_CERT_FILE"), "proxy": os.environ.get("HTTP_PROXY"), "exposed": os.environ.get("EXPOSED_KEY"), "host": os.environ.get("STORE_HOST_ONLY")}
with open(sys.argv[-1], "w") as output:
    json.dump(report, output)
sys.stdout.write(value)
sys.stderr.write(value)
`
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "STORE_KEY", Store: []string{"/usr/bin/python3", "-c", script, literal, report}}})
	t.Setenv("STORE_HOST_ONLY", "host-value-must-not-be-inherited")
	guest := &Init{memberStarts: map[string]bool{"store234": true}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	const sentinel = "sentinel-with-no-newline"
	if err := guest.storeBoundSecrets(ctx, "store234", kind, filepath.Join(root, "work"), base, map[string]api.Secret{"STORE_KEY": sentinel}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Input, Cwd, Home, CA, Proxy string
		Key                         *string
		Argv                        []string
		UID, GID                    int
		Groups                      []int
		Exposed, Host               *string
	}
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Input != sentinel || actual.Key != nil || !reflect.DeepEqual(actual.Argv, []string{literal, report}) || actual.UID != AgentUID || actual.GID != AgentGID || actual.Cwd != filepath.Join(root, "work") || actual.Home != envValue(base, "HOME") || actual.CA != envValue(base, "SSL_CERT_FILE") || actual.Proxy != envValue(base, "HTTP_PROXY") || actual.Exposed != nil || actual.Host != nil {
		t.Fatalf("Store input, ownership or environment escaped its contract: %+v", actual)
	}
	for _, group := range actual.Groups {
		if group != AgentGID {
			t.Fatal("Store retained a foreign supplementary group")
		}
	}
	guest.mu.Lock()
	defer guest.mu.Unlock()
	if len(guest.owned) != 0 || !guest.memberStarts["store234"] {
		t.Fatal("Store returned before cleanup or consumed its caller's member reservation")
	}
}

const ownedStoreBarrierScript = `import os, sys, time
sys.stdin.read()
with open(sys.argv[1], "w") as ready:
    ready.write("ready")
while not os.path.exists(sys.argv[2]):
    time.sleep(0.005)
`

func TestOwnedStoreSharesOneBatchDeadlineAcrossCommands(t *testing.T) {
	root, base := ownedStoreDirectories(t)
	readyOne, readyTwo := filepath.Join(root, "work", "one-ready"), filepath.Join(root, "work", "two-ready")
	gateOne, gateTwo := filepath.Join(root, "work", "one-gate"), filepath.Join(root, "work", "two-gate")
	kind := storeAgentFixture(t, []agents.SecretBinding{
		{Env: "STORE_ONE", Store: []string{"/usr/bin/python3", "-c", ownedStoreBarrierScript, readyOne, gateOne}},
		{Env: "STORE_TWO", Store: []string{"/usr/bin/python3", "-c", ownedStoreBarrierScript, readyTwo, gateTwo}},
	})
	guest := &Init{memberStarts: map[string]bool{"store234": true}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); guest.stopOwned() })
	done := make(chan error, 1)
	go func() {
		done <- guest.storeBoundSecrets(ctx, "store234", kind, filepath.Join(root, "work"), base, map[string]api.Secret{"STORE_ONE": "one-sentinel", "STORE_TWO": "two-sentinel"})
	}()
	first := waitForOwnedStore(t, guest, readyOne)
	deadlineOne, limited := first.ctx.Deadline()
	if !limited || time.Until(deadlineOne) <= 0 || time.Until(deadlineOne) > 30*time.Second {
		t.Fatal("Store inherited the ordinary exec timeout instead of a bounded batch")
	}
	if err := os.WriteFile(gateOne, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	second := waitForOwnedStore(t, guest, readyTwo)
	deadlineTwo, limited := second.ctx.Deadline()
	if !limited || !deadlineOne.Equal(deadlineTwo) {
		t.Fatal("a later Store command restarted its batch time budget")
	}
	if err := os.WriteFile(gateTwo, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := awaitOwnedStore(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedStoreHonoursEarlierCallerLifetimeAndJoinsCancellation(t *testing.T) {
	root, base := ownedStoreDirectories(t)
	ready, gate := filepath.Join(root, "work", "ready"), filepath.Join(root, "work", "gate")
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "STORE_KEY", Store: []string{"/usr/bin/python3", "-c", ownedStoreBarrierScript, ready, gate}}})
	guest := &Init{memberStarts: map[string]bool{"store234": true}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(func() { cancel(); guest.stopOwned() })
	done := make(chan error, 1)
	go func() {
		done <- guest.storeBoundSecrets(ctx, "store234", kind, filepath.Join(root, "work"), base, map[string]api.Secret{"STORE_KEY": "private-sentinel-marker"})
	}()
	run := waitForOwnedStore(t, guest, ready)
	want, _ := ctx.Deadline()
	actual, limited := run.ctx.Deadline()
	if !limited || !actual.Equal(want) {
		t.Fatal("Store relaxed an earlier caller deadline")
	}
	cancel()
	requireSecretStoreError(t, awaitOwnedStore(t, done), "private-sentinel-marker", ownedStoreBarrierScript)
	select {
	case <-run.procDone:
	default:
		t.Fatal("Store cancellation returned before the physical owned waiter")
	}
	guest.mu.Lock()
	defer guest.mu.Unlock()
	if len(guest.owned) != 0 {
		t.Fatal("a cancelled Store command retained process ownership")
	}
}

func TestOwnedStoreOutputOverflowCancelsAndSuppressesChildDiagnostics(t *testing.T) {
	root, base := ownedStoreDirectories(t)
	const script = `import sys, time
value = sys.stdin.read()
sys.stdout.write(value + "x" * (64 * 1024 + 1))
sys.stdout.flush()
sys.stderr.write(value)
sys.stderr.flush()
time.sleep(60)
`
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "STORE_KEY", Store: []string{"/usr/bin/python3", "-c", script}}})
	guest := &Init{memberStarts: map[string]bool{"store234": true}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(func() { cancel(); guest.stopOwned() })
	err := guest.storeBoundSecrets(ctx, "store234", kind, filepath.Join(root, "work"), base, map[string]api.Secret{"STORE_KEY": "private-sentinel-marker"})
	requireSecretStoreError(t, err, "private-sentinel-marker", script, strings.Repeat("x", 32))
	if ctx.Err() != nil {
		t.Fatal("output overflow waited for the outer lifetime instead of cancelling the owned command")
	}
	guest.mu.Lock()
	defer guest.mu.Unlock()
	if len(guest.owned) != 0 {
		t.Fatal("output overflow returned before owned process cleanup")
	}
}

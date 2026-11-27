package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func h5GrantState(t *testing.T) Host {
	t.Helper()
	isolateH5GrantHome(t)
	h := Host{Name: "box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierOwn, strings.Repeat("a", 32), ""); err != nil {
		t.Fatal(err)
	}
	return h
}

func h5Policy() SandboxPolicy {
	return SandboxPolicy{SessionID: "abc23def", HostID: strings.Repeat("a", 32), Ceiling: TierThrowaway,
		Secrets: []string{"API_KEY"}, CreatedAt: time.Now().UTC(), request: "one-request"}
}

func TestSandboxCeilingCannotBeRaisedOrReplacedByAnotherRequest(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	p.Ceiling = TierOwn
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	tier, got, err := EffectiveSessionTier(t.Context(), h, p.SessionID, p.HostID)
	if err != nil || got == nil || tier != TierThrowaway || got.Ceiling != TierThrowaway {
		t.Fatalf("the saved ceiling was raised: tier=%s policy=%+v err=%v", tier, got, err)
	}
	p.request = "another-request"
	if err := reserveSandbox(t.Context(), h, p); !errors.Is(err, errSandboxIDTaken) {
		t.Fatalf("another request replaced a policy: %v", err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierShared, "", ""); err != nil {
		t.Fatal(err)
	}
	tier, _, err = EffectiveSessionTier(t.Context(), h, p.SessionID, p.HostID)
	if err != nil || tier != TierShared {
		t.Fatalf("the host's stricter tier was ignored: %s, %v", tier, err)
	}
}

func TestSandboxPolicyKeepsUnknownFieldsAndRejectsIdentityChanges(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	if err := withStateFile(t.Context(), []string{"hosts", HostFileKey(h.Name)}, "sandboxes.json", true,
		func(raw []byte, _ bool) ([]byte, error) {
			var root map[string]json.RawMessage
			_ = json.Unmarshal(raw, &root)
			root["future_root"] = json.RawMessage(`{"enabled":true}`)
			var entries map[string]map[string]json.RawMessage
			_ = json.Unmarshal(root["sessions"], &entries)
			entries[p.SessionID]["future_session"] = json.RawMessage(`"kept"`)
			root["sessions"], _ = json.Marshal(entries)
			return json.Marshal(root)
		}); err != nil {
		t.Fatal(err)
	}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(StateDir(), "hosts", HostFileKey(h.Name), "sandboxes.json")
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "future_root") || !strings.Contains(string(raw), "future_session") {
		t.Fatalf("newer policy fields were dropped: %v", err)
	}
	if _, _, err := EffectiveSessionTier(t.Context(), h, p.SessionID, strings.Repeat("b", 32)); err == nil {
		t.Fatal("a policy was applied to another host identity")
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"sessions":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSandboxPolicy(t.Context(), h, p.SessionID); err == nil {
		t.Fatal("a damaged policy became an unrestricted session")
	}
}

func TestSandboxPolicyPathCannotFollowAnotherHostNamespace(t *testing.T) {
	h := h5GrantState(t)
	outside := t.TempDir()
	marker := filepath.Join(outside, "kept")
	if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(StateDir(), "hosts")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, HostFileKey(h.Name))); err != nil {
		t.Skipf("cannot make the hostile link on this system: %v", err)
	}
	if err := reserveSandbox(t.Context(), h, h5Policy()); err == nil {
		t.Fatal("a policy was written through a host namespace link")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "outside" {
		t.Fatalf("the outside file changed: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "sandboxes.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a policy escaped its host namespace: %v", err)
	}
}

type h5ObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *h5ObservedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.observed) })
	return err
}

func TestGrantStateCancellationDoesNotWaitBehindAnotherWriter(t *testing.T) {
	h5GrantState(t)
	mu := stateProcessMutex(nil, grantsFileName)
	mu.Lock()
	defer mu.Unlock()
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &h5ObservedContext{Context: base, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- withStateFile(ctx, nil, grantsFileName, false, func([]byte, bool) ([]byte, error) { return nil, nil })
	}()
	<-ctx.observed
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled read waited for the other writer")
	}
}

func TestGrantCounterOverflowCannotPublishCorruptHistory(t *testing.T) {
	h := h5GrantState(t)
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := GrantRecord{HostName: h.Name, HostID: strings.Repeat("a", 32), Kind: OutboundSecret,
		Name: "API_KEY", Count: int(^uint(0) >> 1), FirstAt: now, LastAt: now}
	raw, err := json.Marshal(grantsFile{Version: 1, Grants: []GrantRecord{r}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(StateDir(), grantsFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordGrantContext(t.Context(), h, r.HostID, Outbound{Kind: r.Kind, Name: r.Name}); err == nil {
		t.Fatal("an overflowing count was published")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(raw) {
		t.Fatalf("overflow changed the ledger: %v", err)
	}
}

func TestGrantWriterProcess(t *testing.T) {
	state := os.Getenv("GRANT_WRITER_STATE")
	if state == "" {
		return
	}
	t.Setenv("XDG_STATE_HOME", state)
	count, err := strconv.Atoi(os.Getenv("GRANT_WRITER_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	for range count {
		if err := RecordGrant(Host{Name: "box"}, "", Outbound{Kind: OutboundSecret, Name: "API_KEY"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGrantLedgerKeepsEveryProcessIncrement(t *testing.T) {
	h := h5GrantState(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const processes, each = 4, 8
	children := make([]*exec.Cmd, processes)
	for i := range children {
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestGrantWriterProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "GRANT_WRITER_STATE="+os.Getenv("XDG_STATE_HOME"), "GRANT_WRITER_COUNT="+strconv.Itoa(each))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children[i] = cmd
	}
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := GrantsForHostContext(t.Context(), h, "")
	if err != nil || len(rows) != 1 || rows[0].Count != processes*each {
		t.Fatalf("process increments were lost: %+v, %v", rows, err)
	}
}

func isolateH5GrantHome(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
}

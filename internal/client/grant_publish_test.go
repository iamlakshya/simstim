package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type grantWriteFunc func([]byte) (int, error)

func (f grantWriteFunc) Write(p []byte) (int, error) { return f(p) }

func TestNewRecordsBeforePublicationAndNeverStoresValue(t *testing.T) {
	h := h5GrantState(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const value = "fixture-secret-value-for-record-before-send"
	published := false
	c := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"session.id", "secrets"},
		pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	c.w = proto.NewWriter(grantWriteFunc(func(p []byte) (int, error) {
		published = true
		grants, err := GrantsForHostContext(t.Context(), h, c.HostID())
		if err != nil || len(grants) != 1 || grants[0].Name != "API_KEY" {
			t.Errorf("publication preceded durable grant: %+v, %v", grants, err)
		}
		_, release, err := LockHost(t.Context(), h.Name, "competing mutation")
		if err == nil {
			release()
			t.Error("host mutation was possible during frame publication")
		}
		if !bytes.Contains(p, []byte(value)) {
			t.Error("fixture did not exercise a secret-bearing wire request")
		}
		cancel()
		return len(p), nil
	}))
	_, err := c.NewSession(ctx, NewArgs{Secrets: map[string]api.Secret{"API_KEY": value}})
	if !published || !errors.Is(err, context.Canceled) {
		t.Fatalf("new: published=%v err=%v", published, err)
	}
	if err := filepath.WalkDir(StateDir(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte(value)) {
			t.Errorf("secret value was stored in %s", filepath.Base(path))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCallWithSentReleasesBeforeWaitingAndCleansFailedPending(t *testing.T) {
	h := h5GrantState(t)
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "failed"}[fail], func(t *testing.T) {
			ctx, release, err := (&Conn{Host: h, hostID: strings.Repeat("a", 32)}).beginGrant(t.Context(), "new", false)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			var req proto.Request
			c := &Conn{Host: h, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
			c.w = proto.NewWriter(grantWriteFunc(func(p []byte) (int, error) {
				if fail {
					return 0, io.ErrClosedPipe
				}
				frame, err := proto.Read(bufio.NewReader(bytes.NewReader(p)))
				if err != nil {
					return 0, err
				}
				if err := json.Unmarshal(frame.Payload, &req); err != nil {
					return 0, err
				}
				return len(p), nil
			}))
			released := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				result <- c.callWithSent(ctx, "fixture", nil, nil, func() { release(); close(released) })
			}()
			select {
			case <-released:
			case <-t.Context().Done():
				t.Fatal("publication kept the operation lock")
			}
			_, unlock, err := LockHost(t.Context(), h.Name, "post-publication mutation")
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			c.mu.Lock()
			pending := c.pending[req.ID]
			count := len(c.pending)
			c.mu.Unlock()
			if fail {
				if count != 0 {
					t.Fatal("failed write leaked a pending request")
				}
				if err := <-result; !errors.Is(err, ErrClosed) {
					t.Fatalf("failed write = %v", err)
				}
				return
			}
			if pending == nil {
				t.Fatal("request completed before its response")
			}
			pending <- proto.Response{ID: req.ID, OK: true}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStaleConnectionCannotSendAfterHostRemovalOrIdentityReplacement(t *testing.T) {
	h := h5GrantState(t)
	c := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"session.id"}}
	if err := SetTierContext(t.Context(), h.Name, TierShared, strings.Repeat("b", 32), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.beginGrant(t.Context(), "new", false); err == nil {
		t.Fatal("stale connection retained another identity's grants")
	}
	if err := UpdateHosts(t.Context(), HostChange{Kind: "test forget", Was: &h}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.beginGrant(t.Context(), "new", false); err == nil {
		t.Fatal("forgotten host remained eligible to receive grants")
	}
}

func TestRequestedWallCannotFallBackToAnUnwalledNew(t *testing.T) {
	for _, wall := range []string{"auto", "process", "gvisor", "microvm", "invalid", " NONE", ""} {
		err := CheckRequestedWall(wall)
		if wall == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		var ue *UserError
		if !errors.As(err, &ue) || ue.Hint == "" {
			t.Fatalf("%q was accepted: %v", wall, err)
		}
		_, err = (&Conn{}).NewSession(t.Context(), NewArgs{Wall: wall})
		if !errors.As(err, &ue) {
			t.Fatalf("%q entered new without a wall: %v", wall, err)
		}
	}
}

func TestAgentSocketSecretStopsAllCommandsBeforeAnyValueIsFetched(t *testing.T) {
	isolateH5GrantHome(t)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	settings := Settings{Secrets: map[string]SecretDefinition{"API_KEY": {Run: "touch " + ShellWord(marker) + "; printf generated-fixture"}, "SSH_AUTH_SOCK": {Run: "printf forbidden-socket"}}}
	_, err := FetchSecrets(t.Context(), settings, []string{"API_KEY", "SSH_AUTH_SOCK"}, false)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "bad_secret" {
		t.Fatalf("reserved socket secret=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an earlier command ran before all secret names passed the gate")
	}
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		if err := Allow(Host{Name: "box"}, tier, Outbound{Kind: OutboundSecret, Name: "SSH_AUTH_SOCK", Confirmed: true}, settings); err == nil {
			t.Fatalf("%s accepted a socket secret", tier)
		}
	}
}

func TestGrantSnapshotNormalisesAnEmptyRepositoryAllowlist(t *testing.T) {
	h := h5GrantState(t)
	h.Repos = []string{}
	conn := &Conn{Host: h, hostID: strings.Repeat("a", 32)}
	_, release, err := conn.beginGrant(t.Context(), "new", false)
	if err != nil {
		t.Fatalf("a semantically unchanged empty allowlist looked stale: %v", err)
	}
	release()
}

func TestImpossibleAgentGrantIsRefusedBeforeASecretQuestion(t *testing.T) {
	h := h5GrantState(t)
	if err := SetTierContext(t.Context(), h.Name, TierShared, "", ""); err != nil {
		t.Fatal(err)
	}
	config := "[secrets]\nAPI_KEY = \"printf generated-fixture\"\n[secret_tiers]\nAPI_KEY = \"shared\"\n"
	if err := os.WriteFile(settingsPath(), []byte(strings.ReplaceAll(config, "\\n", "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := &Conn{Host: h, hostID: strings.Repeat("a", 32)}
	ctx := WithGrantRequest(t.Context(), NewGrantRequest(false, true))
	err := conn.CheckNewGrants(ctx, []string{"API_KEY"}, true)
	var question *ConfirmError
	var refused *UserError
	if errors.As(err, &question) || !errors.As(err, &refused) || refused.Kind != KindRefused {
		t.Fatalf("impossible agent request asked for a secret approval: %v", err)
	}
}

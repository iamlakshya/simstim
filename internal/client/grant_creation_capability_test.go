package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func replyingCreationConn(t *testing.T, h Host, features []string, observe func(NewArgs)) *Conn {
	t.Helper()
	c := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: features,
		pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	c.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) {
		frame, err := proto.Read(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return 0, err
		}
		var request proto.Request
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return 0, err
		}
		var args NewArgs
		if request.Op != "new" || json.Unmarshal(request.Args, &args) != nil {
			t.Fatal("fixture did not observe an actual new request")
		}
		if observe != nil {
			observe(args)
		}
		id := args.ID
		if id == "" {
			id = "abc234de"
		}
		result, err := json.Marshal(Session{ID: id, State: api.StateStarting, Alive: true})
		if err != nil {
			return 0, err
		}
		c.mu.Lock()
		response := c.pending[request.ID]
		c.mu.Unlock()
		response <- proto.Response{ID: request.ID, OK: true, Result: result}
		return len(data), nil
	}))
	return c
}

func TestLegacyHelperCannotFetchOrPublishOwnTierCreationValues(t *testing.T) {
	h := h5GrantState(t)
	marker := filepath.Join(t.TempDir(), "own-value-command")
	config := fmt.Sprintf("[secrets]\nAPI_KEY = %q\n", "touch "+ShellWord(marker)+"; printf generated-fixture")
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	writes := 0
	c := replyingCreationConn(t, h, []string{"secrets"}, func(NewArgs) { writes++ })
	err := c.CheckNewGrants(t.Context(), []string{"API_KEY"}, false)
	if err == nil {
		_, err = FetchHostSecrets(t.Context(), h, []string{"API_KEY"}, false)
	}
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "helper_old" || !strings.Contains(refusal.Hint, "host add") {
		t.Fatalf("legacy prefetch did not require the precise helper upgrade: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy helper caused an own-tier command before creation authority could be recorded")
	}
	_, err = c.NewSession(t.Context(), NewArgs{Secrets: map[string]api.Secret{"API_KEY": "generated-direct-value"}})
	if !errors.As(err, &refusal) || refusal.Code != "helper_old" || writes != 0 {
		t.Fatalf("final publication bypassed missing session.id: writes=%d error=%v", writes, err)
	}
}

func TestLegacyNoSecretAndScopedCreationPathsRemainReal(t *testing.T) {
	h := h5GrantState(t)
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprint(scoped), func(t *testing.T) {
			var features, names []string
			var values map[string]api.Secret
			if scoped {
				features, names = []string{"secrets"}, []string{"SCOPED_KEY"}
				values = map[string]api.Secret{"SCOPED_KEY": "generated-scoped-value"}
				if err := os.WriteFile(settingsPath(), []byte("[secret_tiers]\nSCOPED_KEY = \"throwaway\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			c := replyingCreationConn(t, h, features, func(a NewArgs) {
				writes++
				if a.ID != "" {
					t.Error("legacy helper was given a fabricated client-reserved identity")
				}
			})
			if err := c.CheckNewGrants(t.Context(), names, false); err != nil {
				t.Fatal(err)
			}
			if _, err := c.NewSession(t.Context(), NewArgs{Secrets: values}); err != nil || writes != 1 {
				t.Fatalf("legacy supported path did not start: writes=%d error=%v", writes, err)
			}
		})
	}
}

func TestCurrentHelperPublishesOnlyAfterSchemaTwoCreation(t *testing.T) {
	h := h5GrantState(t)
	r, err := NewWallGrantRequest(false, false, "none", []string{"API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	writes := 0
	c := replyingCreationConn(t, h, []string{"session.id", "secrets"}, func(a NewArgs) {
		writes++
		if !api.ValidSessionID(a.ID) {
			t.Fatal("current helper request did not carry the reserved identity")
		}
		policy, err := LoadSandboxPolicy(t.Context(), h, a.ID)
		if err != nil || policy == nil || policy.CreationWall != "none" || len(policy.Exposed) != 1 || policy.Exposed[0] != "API_KEY" {
			t.Fatalf("actual frame preceded immutable creation policy: %+v, %v", policy, err)
		}
		var busy *UserError
		if err := SetTierContext(t.Context(), h.Name, TierShared, "", ""); !errors.As(err, &busy) || busy.Code != "host_busy" {
			t.Fatalf("publication did not retain canonical host ownership: %v", err)
		}
	})
	if err := c.CheckNewGrants(ctx, []string{"API_KEY"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewSession(ctx, NewArgs{Secrets: map[string]api.Secret{"API_KEY": "generated-current-value"}}); err != nil || writes != 1 {
		t.Fatalf("current helper producer did not complete: writes=%d error=%v", writes, err)
	}
}

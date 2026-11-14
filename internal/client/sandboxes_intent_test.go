package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCancelledOrFailedIntentMigrationKeepsLegacyBytes(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			h := h5GrantState(t)
			legacy := legacySandboxFixture(t, h, h5Policy())
			writeSandboxFixture(t, h, legacy)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("creation intent transaction failed")
			err := withSandboxFile(ctx, h, true, func(f *sandboxFile) error {
				if f.schema != 2 {
					t.Error("the fixture did not reach the schema migration")
				}
				if cancelled {
					cancel()
					return nil
				}
				return failure
			})
			want := failure
			if cancelled {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("migration returned %v, want %v", err, want)
			}
			after, readError := os.ReadFile(sandboxStatePath(h))
			if readError != nil || !bytes.Equal(after, legacy) {
				t.Fatalf("failed migration replaced legacy creation bytes: %v", readError)
			}
		})
	}
}

func sandboxStatePath(h Host) string {
	return filepath.Join(StateDir(), "hosts", HostFileKey(h.Name), "sandboxes.json")
}

func writeSandboxFixture(t *testing.T, h Host, raw []byte) {
	t.Helper()
	if err := withStateFile(t.Context(), []string{"hosts", HostFileKey(h.Name)}, "sandboxes.json", true,
		func([]byte, bool) ([]byte, error) { return raw, nil }); err != nil {
		t.Fatal(err)
	}
}

func legacySandboxFixture(t *testing.T, h Host, p SandboxPolicy) []byte {
	t.Helper()
	entry, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(),
		Secrets: p.Secrets, CreatedAt: p.CreatedAt, Request: p.request})
	if err != nil {
		t.Fatal(err)
	}
	// Literal fixture bytes avoid using the encoder whose preservation is
	// under test. These unknown values deliberately keep noncanonical JSON.
	entry = append(entry[:len(entry)-1], []byte(`,"future_session":{ "number" : 1e+02 }}`)...)
	return fmt.Appendf(nil, `{"schema":1,"host":%q,"future_root":{ "nested" : [1.00, {"x":"y"}] },"sessions":{%q:%s}}`, h.Name, p.SessionID, entry)
}

func TestSandboxSchemaUpgradePreservesUnknownBytesAndLegacyCreation(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	legacy := legacySandboxFixture(t, h, p)
	writeSandboxFixture(t, h, legacy)
	got, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || got == nil || got.CreationWall != "none" || len(got.Exposed) != 0 {
		t.Fatalf("legacy creation = %+v, %v", got, err)
	}
	before, err := os.ReadFile(sandboxStatePath(h))
	if err != nil || !bytes.Equal(before, legacy) {
		t.Fatal("reading a legacy creation rewrote it")
	}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(sandboxStatePath(h))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(after, &root); err != nil || string(root["schema"]) != "2" {
		t.Fatalf("new writer did not publish the older-reader refusal barrier: %s, %v", after, err)
	}
	if !bytes.Equal(root["future_root"], []byte(`{ "nested" : [1.00, {"x":"y"}] }`)) {
		t.Fatalf("unknown root bytes changed: %s", root["future_root"])
	}
	var sessions map[string]map[string]json.RawMessage
	if err := json.Unmarshal(root["sessions"], &sessions); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sessions[p.SessionID]["future_session"], []byte(`{ "number" : 1e+02 }`)) {
		t.Fatal("unknown session value bytes changed")
	}
	intent := json.RawMessage(`{ "level" : "none", "future" : { "literal" : 1.00 } }`)
	old := []byte(`"wall_intent":{"level":"none"}`)
	if !bytes.Contains(after, old) {
		t.Fatal("fixture cannot locate its canonical creation intent")
	}
	after = bytes.Replace(after, old, append([]byte(`"wall_intent":`), intent...), 1)
	writeSandboxFixture(t, h, after)
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(sandboxStatePath(h))
	if err != nil || !bytes.Contains(retained, intent) {
		t.Fatalf("retry changed immutable intent or its unknown nested bytes: %v", err)
	}
}

func TestSandboxCreationWallAndExposureCannotChangeOnRetry(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	p.CreationWall, p.Exposed = "process", []string{"API_KEY"}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"wall", "add exposure", "remove exposure"} {
		t.Run(change, func(t *testing.T) {
			retry := p
			switch change {
			case "wall":
				retry.CreationWall = "none"
			case "add exposure":
				retry.Exposed = []string{"API_KEY", "OTHER_KEY"}
			case "remove exposure":
				retry.Exposed = nil
			}
			var refusal *UserError
			if err := reserveSandbox(t.Context(), h, retry); !errors.As(err, &refusal) || refusal.Code != "wall_intent" {
				t.Fatalf("changed immutable creation = %v", err)
			}
			got, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
			if err != nil || got.CreationWall != "process" || !slices.Equal(got.Exposed, []string{"API_KEY"}) {
				t.Fatalf("a refused retry changed creation authority: %+v, %v", got, err)
			}
		})
	}
	p.Ceiling = TierOwn
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || got.Ceiling != TierThrowaway {
		t.Fatalf("creation schema weakened the existing ceiling minimum: %+v, %v", got, err)
	}
}

func TestSchemaTwoRequiresUnambiguousValidCreationIntent(t *testing.T) {
	p := h5Policy()
	entry, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(),
		CreatedAt: p.CreatedAt, Request: p.request})
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range []string{"null", `{}`, `{"level":"auto"}`, `{"level":"none","exposed":null}`,
		`{"level":"process","exposed":["SSH_AUTH_SOCK"]}`, `{"level":"process","exposed":["B","A"]}`,
		`{"level":"process","exposed":["A","A"]}`, `{"level":"none","level":"process"}`,
		`{"Level":"none"}`, `{"level":"none","expoſed":["A"]}`} {
		raw := append(bytes.Clone(entry[:len(entry)-1]), []byte(`,"wall_intent":`+intent+`}`)...)
		if _, err := decodeSandbox(p.SessionID, raw, 2); err == nil {
			t.Fatalf("invalid schema2 creation accepted: %s", intent)
		}
	}
	if _, err := decodeSandbox(p.SessionID, entry, 2); err == nil {
		t.Fatal("schema2 silently treated a missing intent as unwalled")
	}
	valid := append(bytes.Clone(entry[:len(entry)-1]), []byte(`,"wall_intent":{"level":"none"}}`)...)
	if _, err := decodeSandbox(p.SessionID, valid, 1); err == nil {
		t.Fatal("schema1 accepted new security metadata without its older-reader barrier")
	}
	if _, err := decodeSandbox(p.SessionID, valid, 3); err == nil {
		t.Fatal("an unknown creation schema became authority")
	}
}

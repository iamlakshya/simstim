package client

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/egress"
)

func receiptProjectionRaw(t *testing.T, p *api.JSONProjection, span api.JSONSpan) []byte {
	t.Helper()
	var out strings.Builder
	if err := p.WalkEncoded(span, func(part []byte) error { _, err := out.Write(part); return err }); err != nil {
		t.Fatal(err)
	}
	return []byte(out.String())
}

func receiptProjectionReferenceCallbacks(t *testing.T, p *api.JSONProjection) (func(api.JSONSpan, api.JSONSpan) error, func(api.JSONSpan, api.JSONSpan) error) {
	t.Helper()
	// Materialising ONLY the test reference; production uses actual SAME
	// WithSources egress validator and counted/indexed name-set aid.
	state := func(value, allowed api.JSONSpan) error {
		var s api.SessionSecretState
		if err := json.Unmarshal(receiptProjectionRaw(t, p, value), &s); err != nil {
			return err
		}
		if !s.Supported() {
			return errSandboxReceiptStateFuture
		}
		var names []string
		if err := json.Unmarshal(receiptProjectionRaw(t, p, allowed), &names); err != nil {
			return err
		}
		local := map[string]bool{}
		for _, name := range names {
			local[name] = true
		}
		_, err := egress.CloneLocalSessionSecretState(&s, local)
		return err
	}
	names := func(original, receipt api.JSONSpan) error {
		var a, b []string
		if original.End != 0 {
			if err := json.Unmarshal(receiptProjectionRaw(t, p, original), &a); err != nil {
				return err
			}
		}
		if err := json.Unmarshal(receiptProjectionRaw(t, p, receipt), &b); err != nil {
			return err
		}
		slices.Sort(a)
		a = slices.Compact(a)
		if !slices.Equal(a, b) {
			return sandboxReceiptError()
		}
		return nil
	}
	return state, names
}

func receiptProjectionFixtureRow(t *testing.T) map[string]any {
	t.Helper()
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := cachefs.CaptureRoot(root)
	if closeErr := root.Close(); err != nil || closeErr != nil {
		t.Fatalf("native metadata fixture: %v/%v", err, closeErr)
	}
	scope := sandboxReceiptScope{Host: "box", Source: hostStateSourceWitness{Version: 1, Exists: true, Generation: strings.Repeat("a", 32), Path: "/original/hosts.toml", Root: ref}, HostID: strings.Repeat("b", 32), MemberID: projectionSession, RootID: projectionSession, RunGeneration: strings.Repeat("c", 32), BackendGeneration: strings.Repeat("d", 32)}
	r := sandboxDeliveryReceipt{Version: 1, Revision: strings.Repeat("e", 32), Creation: strings.Repeat("f", 32), Publication: strings.Repeat("1", 32), Operation: "new", Scope: scope, Allowed: []string{"KEY"}, Supplied: []string{"KEY"}, Missing: []string{},
		State: &api.SessionSecretState{Version: 1, Generation: scope.BackendGeneration, Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}, Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeExposed}}}}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"session_id": projectionSession, "host_id": scope.HostID, "ceiling": "own", "secrets": []string{"KEY"}, "created_at": time.Unix(1, 0).UTC(), "request": r.Creation, "wall_intent": map[string]any{"level": "none"}, "delivery_receipt": receipt}
}

func compareReceiptProjection(t *testing.T, row map[string]any, wantValid bool) {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	full, fullErr := decodeSandboxContext(t.Context(), projectionSession, raw, 4)
	stateRaw, err := json.Marshal(map[string]any{projectionSession: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := api.NewJSONBytesProjection(t.Context(), stateRaw)
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.ObjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	var facts agentSandboxPolicy
	var projectedErr error
	err = p.Object(root, nil, func(f api.JSONField) error {
		var err error
		facts, err = projectAgentRow(t.Context(), p, f.Value, f.Name, 4, false)
		if err != nil {
			return err
		}
		validate, names := receiptProjectionReferenceCallbacks(t, p)
		projectedErr = projectAgentSandboxReceipts(t.Context(), p, f.Value, f.Name, 4, &facts, validate, names, receiptTestRelationPhase(t, p))
		return projectedErr
	})
	if err != nil {
		projectedErr = err
	}
	if (fullErr == nil) != wantValid || (projectedErr == nil) != wantValid {
		t.Fatalf("full/FD receipt validity mismatch: full=%v fd=%v want=%v raw=%s", fullErr, projectedErr, wantValid, raw)
	}
	if wantValid && (full.futureDeliveryReceipt != facts.FutureReceipt || full.futureInputOperation != facts.FutureInput) {
		t.Fatal("future receipt facts differ from full decoder")
	}
}

func TestBoundedReceiptMatchesFullKnownAndFutureGrammar(t *testing.T) {
	for _, kind := range []string{"known", "future wrapper", "future source", "missing source version", "future source bad scalar", "generation mismatch", "partition overlap", "input nonce32", "input known"} {
		t.Run(kind, func(t *testing.T) {
			row := receiptProjectionFixtureRow(t)
			receipt := row["delivery_receipt"].(map[string]any)
			scope := receipt["scope"].(map[string]any)
			source := scope["source"].(map[string]any)
			valid := true
			switch kind {
			case "future wrapper":
				receipt["version"] = 2
				receipt["revision"] = 99
			case "future source":
				source["version"] = 2
			case "missing source version":
				delete(source, "version")
				valid = false
			case "future source bad scalar":
				source["version"] = 2
				receipt["revision"] = 99
				valid = false
			case "generation mismatch":
				scope["backend_generation"] = strings.Repeat("9", 32)
				valid = false
			case "partition overlap":
				receipt["missing_names"] = []string{"KEY"}
				valid = false
			case "input nonce32", "input known":
				delete(row, "delivery_receipt")
				nonce := strings.Repeat("a", 64)
				if kind == "input nonce32" {
					nonce = strings.Repeat("a", 32)
					valid = false
				}
				row["input_operation"] = map[string]any{"version": 1, "revision": strings.Repeat("e", 32), "creation": strings.Repeat("f", 32), "operation": "new", "scope": scope, "nonce": nonce, "digest": strings.Repeat("1", 64), "summary_hash": strings.Repeat("2", 64), "selection_hash": strings.Repeat("3", 64)}
			}
			compareReceiptProjection(t, row, valid)
		})
	}
}

func TestBoundedReceiptOriginalNamesUseExactSortedCompactedSet(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	row["secrets"] = []string{"B", "A", "A"}
	r := row["delivery_receipt"].(map[string]any)
	r["allowed_names"], r["supplied_names"] = []string{"A", "B"}, []string{"A", "B"}
	state := r["state"].(map[string]any)
	state["intent"] = api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"A": api.SecretModeExposed, "B": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}
	state["primary"] = map[string]api.PublicSecretState{"A": {Mode: api.SecretModeExposed}, "B": {Mode: api.SecretModeExposed}}
	compareReceiptProjection(t, row, true)
	r["allowed_names"] = []string{"A", "B", "FOREIGN"}
	compareReceiptProjection(t, row, false)
}

// TEST reference only: production uses the actual counted SAME-store phase.
func receiptTestRelationPhase(t *testing.T, p *api.JSONProjection) sandboxReceiptRelationPhase {
	return func(plan sandboxReceiptRelationPlan, check func(sandboxReceiptRelationLookup) error) error {
		var groups [6]map[string]api.JSONSpan
		for group, span := range plan.Groups {
			groups[group] = make(map[string]api.JSONSpan)
			if span.End == 0 {
				continue
			}
			if group <= int(receiptOriginalBinds) {
				fields, err := p.Fields(span)
				if err != nil {
					return err
				}
				for {
					field, more, err := fields.Next()
					if err != nil {
						return err
					}
					if !more {
						break
					}
					name, err := retainProjectedString(p, field.Name)
					if err != nil {
						return err
					}
					groups[group][name] = field.Value
				}
			} else {
				values, err := p.Values(span)
				if err != nil {
					return err
				}
				for {
					value, more, err := values.Next()
					if err != nil {
						return err
					}
					if !more {
						break
					}
					name, err := retainProjectedString(p, value)
					if err != nil {
						return err
					}
					groups[group][name] = value
				}
			}
		}
		return check(func(group sandboxReceiptRelationGroup, key api.JSONSpan) (api.JSONSpan, bool, error) {
			name, err := retainProjectedString(p, key)
			if err != nil {
				return api.JSONSpan{}, false, err
			}
			value, ok := groups[group][name]
			return value, ok, nil
		})
	}
}

func TestBoundedReceiptUnorderedStateMapsKeepFullParity(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	row["secrets"] = []string{"A", "B"}
	r := row["delivery_receipt"].(map[string]any)
	r["allowed_names"], r["supplied_names"] = []string{"A", "B"}, []string{"A", "B"}
	r["state"] = json.RawMessage(`{"version":1,"generation":"dddddddddddddddddddddddddddddddd","intent":{"version":1,"modes":{"B":"exposed","A":"exposed"},"binds":null},"primary":{"B":{"mode":"exposed","armed":false},"A":{"mode":"exposed","armed":false}}}`)
	compareReceiptProjection(t, row, true)
}

type receiptReadFault struct{ err error }

func (r receiptReadFault) ReadAt([]byte, int64) (int, error) { return 0, r.err }
func TestReceiptHexPredicatePropagatesNativeReadFailure(t *testing.T) {
	marker := errors.New("actual native projected source read failed")
	p, err := api.NewJSONProjection(t.Context(), receiptReadFault{marker}, 34)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := receiptProjectionHexValue(p, api.JSONSpan{Start: 0, End: 34}, 32)
	if valid || !errors.Is(err, marker) {
		t.Fatalf("native read failure was downgraded to invalid/future data: valid=%v %v", valid, err)
	}
}

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestFreshLineageV2HasOneFullAndFDGrammar(t *testing.T) {
	for _, kind := range []string{"known", "missing kind", "unknown kind", "unsorted", "duplicate", "self parent", "self root", "aliased kind"} {
		t.Run(kind, func(t *testing.T) {
			row := projectionRow()
			row["ssh_agent"], row["secrets"] = false, []string{"OPENAI_KEY"}
			row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"OPENAI_KEY"}}
			lineage := map[string]any{"version": 2, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"OPENAI_KEY"}, "selected_kind": "codex"}
			switch kind {
			case "missing kind":
				delete(lineage, "selected_kind")
			case "unknown kind":
				lineage["selected_kind"] = "unknown"
			case "unsorted":
				lineage["allowed_names"] = []string{"Z_KEY", "OPENAI_KEY"}
			case "duplicate":
				lineage["allowed_names"] = []string{"OPENAI_KEY", "OPENAI_KEY"}
			case "self parent":
				lineage["parent"] = projectionSession
			case "self root":
				lineage["root"] = projectionSession
			case "aliased kind":
				delete(lineage, "selected_kind")
				lineage["Selected_Kind"] = "codex"
			}
			row["lineage"] = lineage
			row["delivery_intent"] = api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"OPENAI_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}
			raw := projectionState(t, 4, row, nil)
			compareAgentProjection(t, raw)
			file, err := decodeSandboxFile(raw, Host{Name: "box"})
			if (err == nil) != (kind == "known") {
				t.Fatalf("v2 grammar validity %s: %v", kind, err)
			}
			if err == nil {
				p, err := decodeSandbox(projectionSession, file.sessions[projectionSession], 4)
				if err != nil || p.FutureLineage || p.lineage.SelectedKind != "codex" || !reflect.DeepEqual(localSandboxNames(&p), []string{"OPENAI_KEY"}) {
					t.Fatalf("known fresh lineage: %+v %v", p, err)
				}
			}
		})
	}
}

func TestFreshLineageKeepsV1EncodingAndOldSchemaFuture(t *testing.T) {
	v1 := sandboxLineage{Version: 1, Parent: "def234gh", Root: "def234gh", Allowed: []string{"KEY"}}
	encoded, err := json.Marshal(v1)
	if err != nil || string(encoded) != `{"version":1,"parent":"def234gh","root":"def234gh","allowed_names":["KEY"]}` {
		t.Fatalf("v1 stored bytes changed: %s %v", encoded, err)
	}
	legacyExtra := []byte(`{"version":1,"parent":"def234gh","root":"def234gh","allowed_names":["KEY"],"Selected_Kind":{"old_unknown":true}}`)
	if known, future, err := decodeSandboxLineageContext(t.Context(), projectionSession, legacyExtra, 3, false); err != nil || future || known == nil || known.SelectedKind != "" {
		t.Fatal("adding v2 tightened an unknown v1 field into a new alias rule", err)
	}
	v2 := []byte(`{ "version":2,"parent":"def234gh","root":"def234gh","allowed_names":["KEY"],"selected_kind":"codex" }`)
	if known, future, err := decodeSandboxLineageContext(t.Context(), projectionSession, v2, 3, false); err != nil || !future || known != nil {
		t.Fatal("old schema adopted v2 authority")
	}
	futureRaw := []byte(`{"version":2,"parent":"def234gh","root":"def234gh","allowed_names":["KEY"],"selected_kind":"codex","future":{"opaque":[1,2]}}`)
	if known, future, err := decodeSandboxLineageContext(t.Context(), projectionSession, futureRaw, 4, false); err != nil || !future || known != nil {
		t.Fatal("unknown v2 extension became ordinary lineage")
	}
}

type freshReservationFixtureProof struct {
	conn     *Conn
	snapshot *freshSandboxReservationSnapshot
	cohort   *api.SandboxCohortView
	held     bool
}

// Generated lower storage/locking oracle only; Main's actual local selection,
// C4 answer, protected live cohort and first frame establish real authority.
func (p *freshReservationFixtureProof) holdFreshSandboxReservation(ctx context.Context, intent *freshSandboxReservationIntent) (*api.SandboxCohortView, func(), error) {
	if intent.conn != p.conn || intent.child.lineage.SelectedKind != "codex" || !reflect.DeepEqual(intent.child.lineage.Allowed, []string{"OPENAI_KEY"}) ||
		!bytes.Equal(intent.snapshot.parentRaw, p.snapshot.parentRaw) || !bytes.Equal(intent.snapshot.rootRaw, p.snapshot.rootRaw) {
		return nil, nil, joinIntentError()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	p.held = true
	return p.cohort.Clone(), func() { p.held = false }, nil
}

func freshReservationFixture(t *testing.T) (Host, context.Context, *Conn, *freshSandboxReservationSnapshot, SandboxPolicy, *freshReservationFixtureProof) {
	t.Helper()
	h, ctx, o, _ := hostObservationFixture(t)
	c := o.checkedConn
	if err := commitSandboxBoundSchema(ctx, h); err != nil {
		t.Fatal(err)
	}
	root := SandboxPolicy{SessionID: "abcd2345", HostID: c.HostID(), Ceiling: TierOwn, CreationWall: "process", Exposed: []string{"ANTHROPIC_KEY"}, Secrets: []string{"ANTHROPIC_KEY"},
		PolicySources: json.RawMessage(`{ "original_policy_and_resources" : {"duplicate":1,"duplicate":2} }`), CreatedAt: time.Unix(1, 0).UTC(), request: strings.Repeat("a", 32),
		DeliveryIntent: &api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"ANTHROPIC_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}}
	if err := reserveSandbox(ctx, h, root); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadSandboxPolicy(ctx, h, root.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	parent := *cloneSandboxPolicy(stored)
	parent.SessionID, parent.request, parent.CreatedAt = "bcde2345", strings.Repeat("b", 32), time.Unix(2, 0).UTC()
	parent.lineage = &sandboxLineage{Version: 1, Parent: root.SessionID, Root: root.SessionID, Allowed: []string{"ANTHROPIC_KEY"}}
	if err := reserveSandbox(ctx, h, parent); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureFreshSandboxReservationContext(ctx, c, parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	child := SandboxPolicy{SessionID: "cdef2345", HostID: c.HostID(), Ceiling: TierOwn, CreationWall: "process", Exposed: []string{"OPENAI_KEY"}, Secrets: []string{"OPENAI_KEY"},
		PolicySources: bytes.Clone(root.PolicySources), CreatedAt: time.Unix(3, 0).UTC(), request: strings.Repeat("c", 32),
		lineage:        &sandboxLineage{Version: 2, Parent: parent.SessionID, Root: root.SessionID, Allowed: []string{"OPENAI_KEY"}, SelectedKind: "codex"},
		DeliveryIntent: &api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"OPENAI_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}}
	cohort := &api.SandboxCohortView{Snapshot: sharedGrantSnapshot(root.SessionID, parent.SessionID), RunGeneration: strings.Repeat("d", 32),
		SecretState: &api.SessionSecretState{Version: 1, Generation: strings.Repeat("e", 32), Intent: api.CloneSecretDeliveryIntent(*root.DeliveryIntent), Primary: map[string]api.PublicSecretState{"ANTHROPIC_KEY": {Mode: api.SecretModeExposed}}}}
	proof := &freshReservationFixtureProof{conn: c, snapshot: snapshot, cohort: cohort}
	return h, ctx, c, snapshot, child, proof
}

func TestFreshReservationOwnsOnlyChildDomainAndPreservesParentRootBytes(t *testing.T) {
	h, ctx, c, snapshot, child, proof := freshReservationFixture(t)
	if err := reserveSandbox(ctx, h, child); err == nil {
		t.Fatal("generic DTO minted fresh-local authority")
	}
	previous := beforeGrantsStateWrite
	defer func() { beforeGrantsStateWrite = previous }()
	beforeGrantsStateWrite = func() {
		if !proof.held {
			t.Fatal("actual writer escaped sealed-selection lease")
		}
	}
	if err := reserveFreshSandboxContext(ctx, c, snapshot, child, proof); err != nil {
		t.Fatal(err)
	}
	if proof.held {
		t.Fatal("reservation lease did not join")
	}
	if err := withSandboxFile(ctx, h, false, func(file *sandboxFile) error {
		if file.schema != 4 || !bytes.Equal(file.sessions[snapshot.parent.SessionID], snapshot.parentRaw) || !bytes.Equal(file.sessions[snapshot.root.SessionID], snapshot.rootRaw) {
			t.Fatal("fresh child changed parent/root/barrier")
		}
		p, err := decodeSandboxContext(ctx, child.SessionID, file.sessions[child.SessionID], 4)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(localSandboxNames(&p), []string{"OPENAI_KEY"}) || !reflect.DeepEqual(p.Exposed, []string{"OPENAI_KEY"}) || p.DeliveryIntent.Modes["ANTHROPIC_KEY"] != "" {
			t.Fatal("child borrowed parent names/exposure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFreshReservationRefusesChangedAncestorsSelectionAndModeBindingWidening(t *testing.T) {
	for _, kind := range []string{"root row", "parent row", "selected kind", "new pin", "old exposure"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, c, snapshot, child, proof := freshReservationFixture(t)
			switch kind {
			case "root row", "parent row":
				id := snapshot.root.SessionID
				if kind == "parent row" {
					id = snapshot.parent.SessionID
				}
				if err := withSandboxFile(ctx, h, true, func(f *sandboxFile) error {
					fields, err := sandboxObjectContext(ctx, f.sessions[id])
					if err != nil {
						return err
					}
					fields["ceiling"] = json.RawMessage(`"shared"`)
					f.sessions[id], err = marshalSandboxObject(fields)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "selected kind":
				child.lineage.SelectedKind = "claude"
			case "new pin":
				child.PolicySources = json.RawMessage(`{"refreshed":true}`)
			case "old exposure":
				child.wallIntent = bytes.Clone(snapshot.parent.wallIntent)
			}
			if err := reserveFreshSandboxContext(ctx, c, snapshot, child, proof); err == nil {
				t.Fatal("changed facts donated fresh authority")
			}
			if row, err := LoadSandboxPolicy(ctx, h, child.SessionID); err != nil || row != nil {
				t.Fatalf("refusal installed child: %v", err)
			}
		})
	}
	binding := api.SecretBind{Hosts: []string{"api.example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}
	for _, kind := range []string{"bound to exposed", "exposed to bound", "binding widen"} {
		t.Run(kind, func(t *testing.T) {
			old := api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": binding}}
			next := api.CloneSecretDeliveryIntent(old)
			switch kind {
			case "bound to exposed":
				next.Modes["KEY"] = api.SecretModeExposed
				delete(next.Binds, "KEY")
			case "exposed to bound":
				old.Modes["KEY"] = api.SecretModeExposed
				delete(old.Binds, "KEY")
			case "binding widen":
				b := next.Binds["KEY"]
				b.Hosts = append(b.Hosts, "other.example.com:443")
				next.Binds["KEY"] = b
			}
			if err := checkFreshCohortDelivery(t.Context(), old, next); err == nil {
				t.Fatal("same-name live cohort mode/binding was widened")
			}
		})
	}
}

func TestFreshSchemaPromotionCannotAdoptOldFutureRows(t *testing.T) {
	row := projectionRow()
	row["ssh_agent"] = false
	row["lineage"] = json.RawMessage(`{"version":2,"parent":"def234gh","root":"def234gh","allowed_names":[],"selected_kind":"codex"}`)
	f, err := decodeSandboxFile(projectionState(t, 3, row, nil), Host{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(f.sessions[projectionSession])
	if err := checkSandboxLineageSchemaPromotion(t.Context(), f); err == nil {
		t.Fatal("schema promotion adopted previously opaque fresh lineage")
	}
	if f.schema != 3 || !bytes.Equal(before, f.sessions[projectionSession]) {
		t.Fatal("future row/schema changed on refusal")
	}
}

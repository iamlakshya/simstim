package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func heldRequestSnapshotFixture() api.NetworkHoldSnapshot {
	opened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID, second.Waiting = strings.Repeat("b", 32), 2
	return api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: []api.PendingNetworkRequest{first, second}}
}

// Generated accepted Source plus the actual ordered frame writer. This proves
// client publication/decoding only, not a real egress actor or native transport.
func heldRequestConnectionFixture(t *testing.T, handle func(proto.Request) (any, *proto.Error)) (*Conn, *runtimeDeliveryPeer, Host) {
	t.Helper()
	host := h5GrantState(t)
	connection, peer := newRuntimeDeliveryPeer(t, host.Name, handle)
	connection.Host, connection.hostID = host, strings.Repeat("a", 32)
	connection.proc = &hostStateGeneratedProc{}
	connection.route = route{t: &hostStateGeneratedTransport{}}
	connection.Features = []string{api.FeatureNetworkHolds, "session.id"}
	owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := finishHostTransportObservation(owned, attempt, connection); err != nil {
		t.Fatal(err)
	}
	return connection, peer, host
}

func TestPendingNetworkRequestsUsesOriginalSourceAndCompleteClosedSnapshot(t *testing.T) {
	want := heldRequestSnapshotFixture()
	connection, peer, _ := heldRequestConnectionFixture(t, func(request proto.Request) (any, *proto.Error) {
		var args api.NetworkHoldQueryArgs
		if request.Op != api.OpPolicyPending || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Session != "hold2345" || !api.ExactJSONFields(request.Args, api.NetworkHoldQueryArgs{}) {
			t.Errorf("query lost the exact original session: %s %s", request.Op, request.Args)
		}
		// A real response can need this OP: publication ends at the frame.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, release, err := LockHost(ctx, "box", "pending response fixture")
		if err != nil {
			t.Error("pending query retained publication while awaiting its response", err)
			return nil, &proto.Error{Code: "fixture", Message: "publication was retained", Hint: "Release after the frame."}
		}
		release()
		return want, nil
	})
	got, err := connection.PendingNetworkRequests(t.Context(), "hold2345")
	if err != nil || len(got.Requests) != 2 || got.Requests[0].ID != want.Requests[0].ID || got.Requests[1].ID != want.Requests[1].ID || got.Requests[0].Waiting != 0 || peer.count(api.OpPolicyPending, "") != 1 {
		t.Fatalf("complete masked snapshot changed: %+v %v", got, err)
	}
	if !got.Requests[0].Expires.Equal(want.Requests[0].Expires) {
		t.Fatal("a zero-waiter request received a new deadline")
	}
}

func TestPendingNetworkRequestsRejectsUnknownAndDuplicateMetadata(t *testing.T) {
	for _, kind := range []string{"top alias", "top extra", "entry alias", "duplicate key", "duplicate ID"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := heldRequestSnapshotFixture()
			if kind == "duplicate ID" {
				snapshot.Requests[1].ID = snapshot.Requests[0].ID
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			raw := string(encoded)
			switch kind {
			case "top alias":
				raw = strings.Replace(raw, `"revision":`, `"Revision":`, 1)
			case "top extra":
				raw = strings.Replace(raw, `{`, `{"future":true,`, 1)
			case "entry alias":
				raw = strings.Replace(raw, `"host":`, `"Host":`, 1)
			case "duplicate key":
				raw = strings.Replace(raw, `"revision":1`, `"revision":1,"revision":1`, 1)
			}
			connection, _, _ := heldRequestConnectionFixture(t, func(proto.Request) (any, *proto.Error) { return json.RawMessage(raw), nil })
			got, err := connection.PendingNetworkRequests(t.Context(), "hold2345")
			var user *UserError
			if !errors.As(err, &user) || user.Code != "policy_pending_invalid" || len(got.Requests) != 0 {
				t.Fatalf("invalid snapshot became partial live state: %+v %v", got, err)
			}
		})
	}
}

func TestRefuseNetworkRequestUsesOpaqueIDWithoutTargetOrGrant(t *testing.T) {
	connection, peer, _ := heldRequestConnectionFixture(t, func(request proto.Request) (any, *proto.Error) {
		var args api.NetworkHoldActionArgs
		if request.Op != api.OpPolicyRefuse || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Session != "hold2345" || args.Request != strings.Repeat("c", 32) || args.Scope != "" || !api.ExactJSONFields(request.Args, api.NetworkHoldActionArgs{}) {
			t.Errorf("refusal gained a target or changed its opaque selector: %s %s", request.Op, request.Args)
		}
		return nil, nil
	})
	request := NewNetworkHoldGrantRequest()
	ctx := WithGrantRequest(t.Context(), request)
	defer request.Close()
	if err := connection.RefuseNetworkRequest(ctx, "hold2345", strings.Repeat("c", 32)); err != nil || peer.count(api.OpPolicyRefuse, "") != 1 {
		t.Fatal("original ID refusal failed", err)
	}
}

func TestHeldRequestNonpositiveRejectsWrongPurposeAndReaddedSourceBeforeFrame(t *testing.T) {
	connection, peer, host := heldRequestConnectionFixture(t, func(request proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(request) })
	other := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), other)
	defer other.Close()
	if _, err := connection.PendingNetworkRequests(ctx, "hold2345"); err == nil || len(peer.snapshot()) != 0 {
		t.Fatal("a different purpose donated its request context", err)
	}
	if err := connection.RefuseNetworkRequest(t.Context(), "hold2345", "private target"); err == nil || len(peer.snapshot()) != 0 {
		t.Fatal("display text became a refusal selector", err)
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{host}); err != nil {
		t.Fatal(err)
	}
	if err := connection.RefuseNetworkRequest(t.Context(), "hold2345", strings.Repeat("a", 32)); err == nil || len(peer.snapshot()) != 0 {
		t.Fatal("an identically readded host renewed original Source for a frame", err)
	}
}

func TestPendingNetworkReplyCannotRenewRemovedAndReaddedSource(t *testing.T) {
	snapshot := heldRequestSnapshotFixture()
	connection, _, _ := heldRequestConnectionFixture(t, func(request proto.Request) (any, *proto.Error) {
		if request.Op != api.OpPolicyPending {
			return nil, unexpectedRuntimeRequest(request)
		}
		if err := SaveHosts(t.Context(), nil); err != nil {
			t.Error(err)
		}
		if err := SaveHosts(t.Context(), []Host{{Name: "box"}}); err != nil {
			t.Error(err)
		}
		return snapshot, nil
	})
	if got, err := connection.PendingNetworkRequests(t.Context(), "hold2345"); err == nil || len(got.Requests) != 0 {
		t.Fatalf("a stale response became current pending state: %+v %v", got, err)
	}
}

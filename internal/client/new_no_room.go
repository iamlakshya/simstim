package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type hostAttemptPublicationKeyV1 struct{}

// This owner is made only around the actual final New call. Its body digest
// never enters the wire, diagnostics, logs, grants, manifests or receipts.
type hostAttemptPublicationV1 struct {
	mu             sync.Mutex
	attempt        *HostAttemptV1
	request        *GrantRequest
	conn           *Conn
	plan           *newSecretPlan
	original       context.Context
	nonce          string
	requestID      uint64
	member         string
	body           [32]byte
	source         hostStateSourceWitness
	prepared, sent bool
}

type settledNewRefusalV1 struct {
	owner      *hostAttemptPublicationV1
	settlement api.NewNoRoomSettlementV1
	room       newNoRoomDiagnosticsV1
	receivedAt time.Time
	actual     error // sanitised user error, no control envelope
}

func (e *settledNewRefusalV1) Error() string { return e.actual.Error() }
func (e *settledNewRefusalV1) Unwrap() error { return e.actual }

func hostAttemptNewPublicationContextV1(ctx context.Context, a *HostAttemptV1) (context.Context, error) {
	if ctx == nil || a == nil || a.request == nil || a.conn == nil || grantRequestOf(ctx) != a.request || ctx.Value(hostAttemptPublicationKeyV1{}) != nil {
		return ctx, grantOperationError()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ctx, &UserError{Msg: "could not identify the original host start", Hint: "Keep the original request and try again.", Code: "start_attempt", Kind: KindFailed}
	}
	owner := &hostAttemptPublicationV1{attempt: a, request: a.request, conn: a.conn, original: a.ctx, nonce: hex.EncodeToString(nonce[:])}
	return context.WithValue(ctx, hostAttemptPublicationKeyV1{}, owner), nil
}

// Queue preparation assigns only the harmless random nonce. Source checks and
// the private body snapshot still belong to the actual guarded first writer.
func prepareHostAttemptNewRequestV1(ctx context.Context, c *Conn, request *proto.Request) error {
	if request.Op != "new" {
		return nil
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	a := r.hostAttempt
	r.mu.Unlock()
	owner, _ := ctx.Value(hostAttemptPublicationKeyV1{}).(*hostAttemptPublicationV1)
	if a == nil {
		if owner != nil {
			return grantOperationError()
		}
		return nil
	}
	if owner == nil || owner.attempt != a || owner.request != r || owner.conn != c {
		return grantOperationError()
	}
	if c.HasFeature(api.FeatureNewNoRoomSettlementV1) {
		request.NewAttempt = owner.nonce
	}
	return nil
}

func beforeHostAttemptNewRequestV1(ctx context.Context, c *Conn, request proto.Request, args any) error {
	if request.Op != "new" {
		return nil
	}
	owner, _ := ctx.Value(hostAttemptPublicationKeyV1{}).(*hostAttemptPublicationV1)
	if owner == nil {
		return prepareHostAttemptNewRequestV1(ctx, c, &request)
	}
	actual, ok := args.(NewArgs)
	if !ok || owner.conn != c || owner.request != grantRequestOf(ctx) || request.ID == 0 ||
		owner.attempt == nil || owner.attempt.request != owner.request {
		return grantOperationError()
	}
	if c.HasFeature(api.FeatureNewNoRoomSettlementV1) && request.NewAttempt != owner.nonce {
		return grantOperationError()
	}
	owner.attempt.mu.Lock()
	valid := owner.attempt.running != nil && owner.attempt.newCalled && !owner.attempt.closing && !owner.attempt.started && owner.attempt.conn == c
	owner.attempt.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	if err := checkPreparedPlacementWireV1(ctx, owner.attempt, actual); err != nil {
		return err
	}
	if err := checkHostSelectionPublicationV1(ctx, c); err != nil {
		return err
	}
	source, err := captureHostStateSourceWitness(ctx, c)
	if err != nil {
		return err
	}
	owner.request.mu.Lock()
	plan := owner.request.newSecrets
	owner.request.mu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.prepared || owner.sent {
		return grantOperationError()
	}
	owner.requestID, owner.member, owner.body, owner.source, owner.plan = request.ID, actual.ID, sha256.Sum256(request.Args), source, plan
	owner.prepared = true
	return nil
}

func wroteHostAttemptNewRequestV1(ctx context.Context, c *Conn, id uint64) {
	owner, _ := ctx.Value(hostAttemptPublicationKeyV1{}).(*hostAttemptPublicationV1)
	if owner == nil {
		return
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.conn == c && owner.prepared && owner.requestID == id {
		owner.sent = true
	}
}

// No branch can pass the control envelope into hostErrorOn. Future/malformed
// control data is discarded too, before constructing a public diagnostic.
func receiveHostAttemptNewRefusalV1(ctx context.Context, c *Conn, response proto.Response) error {
	if response.Error == nil {
		return grantOperationError()
	}
	clean := *response.Error
	envelope, decodeErr := api.DecodeNewNoRoomEnvelopeV1(response.Error.Data)
	clean.Data = safeNewNoRoomDiagnosticsV1(response.Error.Data)
	if decodeErr == nil {
		clean.Data = safeNewNoRoomDiagnosticsV1(envelope.Room)
	}
	actual := hostErrorOn(c.Host.Name, &clean)
	owner, _ := ctx.Value(hostAttemptPublicationKeyV1{}).(*hostAttemptPublicationV1)
	if owner == nil || decodeErr != nil || response.OK || clean.Code != "no_room" ||
		!c.HasFeature(api.FeatureNewNoRoomSettlementV1) || ctx.Err() != nil {
		return actual
	}
	owner.mu.Lock()
	valid := owner.prepared && owner.sent && owner.conn == c && owner.request == grantRequestOf(ctx) &&
		owner.requestID == response.ID && envelope.Settlement.RequestID == response.ID &&
		envelope.Settlement.SessionID == owner.member && envelope.Settlement.HostID == c.HostID() &&
		envelope.Settlement.Attempt == owner.nonce && api.ValidSessionID(owner.member)
	source := owner.source
	owner.mu.Unlock()
	if !valid || owner.original == nil || owner.original.Err() != nil || ctx.Err() != nil {
		return actual
	}
	bounded, end, err := narrowHostAttemptContextV1(context.WithoutCancel(owner.original), ctx)
	if err != nil {
		return actual
	}
	defer end()
	if err := withSandboxReceiptObservation(bounded, c, func(owned context.Context) error {
		if err := checkHostSelectionPublicationV1(owned, c); err != nil {
			return err
		}
		return source.check(owned, c)
	}); err != nil || ctx.Err() != nil || owner.original.Err() != nil {
		return actual
	}
	var room newNoRoomDiagnosticsV1
	_ = json.Unmarshal(safeNewNoRoomDiagnosticsV1(envelope.Room), &room)
	return &settledNewRefusalV1{owner: owner, settlement: envelope.Settlement, room: room, receivedAt: time.Now(), actual: actual}
}

// Re-encoding only this closed diagnostics shape prevents control envelopes,
// nonces, hashes or future fields from entering --json or error reporting.
type newNoRoomDiagnosticsV1 struct {
	Known     bool      `json:"accounting_known"`
	BootID    string    `json:"boot_id"`
	At        time.Time `json:"at"`
	Budget    int64     `json:"budget"`
	Reserved  int64     `json:"reserved"`
	Room      int64     `json:"room"`
	Sandboxes int       `json:"sandboxes"`
	Needed    int64     `json:"needed"`
}

func safeNewNoRoomDiagnosticsV1(raw []byte) json.RawMessage {
	var facts newNoRoomDiagnosticsV1
	if len(raw) > 4096 || !api.ExactJSONFields(raw, facts) || json.Unmarshal(raw, &facts) != nil ||
		facts.Budget < 0 || facts.Reserved < 0 || facts.Sandboxes < 0 || facts.Needed < 0 || len(facts.BootID) > 36 {
		return nil
	}
	for _, c := range facts.BootID {
		if c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil
		}
	}
	data, _ := json.Marshal(facts)
	return data
}

func actualSettledNewRefusalV1(err error, a *HostAttemptV1) *settledNewRefusalV1 {
	var proof *settledNewRefusalV1
	if !errors.As(err, &proof) || proof == nil || proof.owner == nil || proof.owner.attempt != a || proof.owner.request != a.request || proof.owner.conn != a.conn {
		return nil
	}
	return proof
}

func hasNewNoRoomControlV1(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, present := fields["new_settlement"]
	return present
}

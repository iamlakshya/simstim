package client

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// These are local historical metadata, never a publication, fetch or input
// capability. Main mints them only from its actual outgoing operation/ACK.
type sandboxReceiptScope struct {
	Host              string                 `json:"host"`
	Source            hostStateSourceWitness `json:"source"`
	HostID            string                 `json:"host_id"`
	MemberID          string                 `json:"member_id"`
	RootID            string                 `json:"root_id"`
	RunGeneration     string                 `json:"run_generation"`
	BackendGeneration string                 `json:"backend_generation,omitempty"`
}

type sandboxDeliveryReceipt struct {
	Version     int                     `json:"version"`
	Revision    string                  `json:"revision"`
	Previous    string                  `json:"previous,omitempty"`
	Creation    string                  `json:"creation"`
	Publication string                  `json:"publication"`
	Operation   string                  `json:"operation"`
	Scope       sandboxReceiptScope     `json:"scope"`
	Allowed     []string                `json:"allowed_names"`
	Supplied    []string                `json:"supplied_names"`
	Missing     []string                `json:"missing_names"`
	State       *api.SessionSecretState `json:"state"`
}

type sandboxInputOperation struct {
	Version       int                 `json:"version"`
	Revision      string              `json:"revision"`
	Previous      string              `json:"previous,omitempty"`
	Creation      string              `json:"creation"`
	Operation     string              `json:"operation"`
	Scope         sandboxReceiptScope `json:"scope"`
	Nonce         api.InputNonce      `json:"nonce"`
	Digest        string              `json:"digest"`
	SummaryHash   string              `json:"summary_hash"`
	SelectionHash string              `json:"selection_hash"`
}

func cloneSandboxDeliveryReceipt(r *sandboxDeliveryReceipt) *sandboxDeliveryReceipt {
	if r == nil {
		return nil
	}
	q := *r
	q.Scope.Source = r.Scope.Source.clone()
	q.Allowed, q.Supplied, q.Missing = slices.Clone(r.Allowed), slices.Clone(r.Supplied), slices.Clone(r.Missing)
	q.State = api.CloneSessionSecretState(r.State)
	return &q
}

func cloneSandboxInputOperation(r *sandboxInputOperation) *sandboxInputOperation {
	if r == nil {
		return nil
	}
	q := *r
	q.Scope.Source = r.Scope.Source.clone()
	return &q
}

func sandboxReceiptError() error {
	return &UserError{Msg: "the local operation receipt does not match its original session", Hint: "Keep the work in quarantine and retry with the original acknowledged operation.", Code: "operation_receipt", Kind: KindRefused}
}

func futureSandboxReceiptError() error {
	return &UserError{Msg: "this newer local operation receipt cannot be used by this client", Hint: "Upgrade the client and retain the original receipt.", Code: "operation_receipt", Kind: KindRefused}
}

func validSandboxReceiptOperation(operation string) bool {
	return operation == "new" || operation == "resume" || operation == "spawn"
}

func validSandboxReceiptDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	var decoded [32]byte
	n, err := hex.Decode(decoded[:], []byte(value))
	return err == nil && n == len(decoded) && hex.EncodeToString(decoded[:]) == value
}

func validateSandboxReceiptHeader(version int, revision, previous, creation, operation string) error {
	if version != 1 || !ValidHostID(revision) || previous != "" && !ValidHostID(previous) ||
		previous == revision || !ValidHostID(creation) || !validSandboxReceiptOperation(operation) {
		return sandboxReceiptError()
	}
	return nil
}

func (s sandboxReceiptScope) validateFields() error {
	if !ValidHostName(s.Host) || !ValidHostID(s.HostID) || !api.ValidSessionID(s.MemberID) || !api.ValidSessionID(s.RootID) ||
		!ValidHostID(s.RunGeneration) || s.BackendGeneration != "" && !ValidHostID(s.BackendGeneration) {
		return sandboxReceiptError()
	}
	return nil
}

func (s sandboxReceiptScope) validate() error {
	if err := s.validateFields(); err != nil {
		return err
	}
	return s.Source.validate()
}

func (s sandboxReceiptScope) matchesPolicy(p *SandboxPolicy) bool {
	return p != nil && s.HostID == p.HostID && s.MemberID == p.SessionID && s.RootID == localSandboxRoot(p)
}

func validateSandboxReceiptNames(ctx context.Context, names []string) error {
	if names == nil {
		return sandboxReceiptError()
	}
	for i, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || i > 0 && names[i-1] >= name {
			return sandboxReceiptError()
		}
	}
	return nil
}

func validateSandboxDeliveryReceipt(ctx context.Context, r *sandboxDeliveryReceipt) error {
	if err := validateSandboxDeliveryReceiptKnownData(ctx, r); err != nil {
		if errors.Is(err, errSandboxReceiptStateFuture) {
			return futureSandboxReceiptError()
		}
		return err
	}
	return r.Scope.Source.validate()
}

// Known v1 fields keep their meaning when an outer extension is unknown.
// This is data validation only; Source support and actual Source checks remain
// separate. A genuinely unsupported public-state DTO stays opaque.
func validateSandboxDeliveryReceiptKnownData(ctx context.Context, r *sandboxDeliveryReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return sandboxReceiptError()
	}
	if err := validateSandboxReceiptHeader(r.Version, r.Revision, r.Previous, r.Creation, r.Operation); err != nil {
		return err
	}
	if !ValidHostID(r.Publication) || r.Scope.BackendGeneration == "" {
		return sandboxReceiptError()
	}
	if err := r.Scope.validateFields(); err != nil {
		return err
	}
	for _, names := range [][]string{r.Allowed, r.Supplied, r.Missing} {
		if err := validateSandboxReceiptNames(ctx, names); err != nil {
			return err
		}
	}
	if r.State == nil || !r.State.Supported() {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errSandboxReceiptStateFuture
	}
	local := make(map[string]bool, len(r.Allowed))
	for _, name := range r.Allowed {
		local[name] = true
	}
	state, err := egress.CloneLocalSessionSecretState(r.State, local)
	if err != nil {
		return err
	}
	if state.Generation != r.Scope.BackendGeneration {
		return sandboxReceiptError()
	}
	seen := make(map[string]bool, len(r.Supplied)+len(r.Missing))
	for _, names := range [][]string{r.Supplied, r.Missing} {
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !local[name] || seen[name] {
				return sandboxReceiptError()
			}
			if _, ok := state.Intent.Modes[name]; !ok {
				return sandboxReceiptError()
			}
			seen[name] = true
		}
	}
	if len(seen) != len(state.Intent.Modes) {
		return sandboxReceiptError()
	}
	return ctx.Err()
}

func validateSandboxInputOperation(ctx context.Context, r *sandboxInputOperation) error {
	if err := validateSandboxInputOperationKnownData(ctx, r); err != nil {
		return err
	}
	return r.Scope.Source.validate()
}

func validateSandboxInputOperationKnownData(ctx context.Context, r *sandboxInputOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return sandboxReceiptError()
	}
	if err := validateSandboxReceiptHeader(r.Version, r.Revision, r.Previous, r.Creation, r.Operation); err != nil {
		return err
	}
	if err := r.Scope.validateFields(); err != nil {
		return err
	}
	if !r.Nonce.Valid() || !validSandboxReceiptDigest(r.Digest) || !validSandboxReceiptDigest(r.SummaryHash) || !validSandboxReceiptDigest(r.SelectionHash) {
		return sandboxReceiptError()
	}
	return ctx.Err()
}

// The acknowledged envelope may only restrict the original local modes and
// binding tuples. Historical Retired keys remain in the original Allowed
// domain, validated by the ONE canonical full-state validator above.
func sandboxReceiptMatchesDelivery(row *SandboxPolicy, receipt *sandboxDeliveryReceipt) bool {
	if row == nil || receipt == nil || receipt.State == nil {
		return false
	}
	for name, mode := range receipt.State.Intent.Modes {
		if row.DeliveryIntent != nil {
			original, ok := row.DeliveryIntent.Modes[name]
			if !ok || original != mode {
				return false
			}
			if mode == api.SecretModeBound && !reflect.DeepEqual(row.DeliveryIntent.Binds[name], receipt.State.Intent.Binds[name]) {
				return false
			}
		} else {
			if mode != api.SecretModeExposed || row.CreationWall != "none" && !slices.Contains(row.Exposed, name) {
				return false
			}
		}
	}
	return true
}

// A new child creation may inherit creation intent, never its parent's ACK or
// current input operation. This only clears a prospective in-memory DTO; the
// sole creation writer cannot remove receipt bytes from an existing row.
func clearSandboxReceiptMetadata(row *SandboxPolicy) {
	if row == nil {
		return
	}
	row.deliveryReceipt, row.inputOperation = nil, nil
	row.deliveryReceiptRaw, row.inputOperationRaw = nil, nil
	row.futureDeliveryReceipt, row.futureInputOperation = false, false
}

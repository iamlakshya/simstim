package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type purgeRemovalReceiptV1 struct {
	Version          int                    `json:"v"`
	Original         Host                   `json:"original"`
	HostID           string                 `json:"host_id"`
	Nonce            string                 `json:"operation_nonce"`
	Source           hostStateSourceWitness `json:"source"`
	Force            bool                   `json:"force"`
	RootMode         RootStepMode           `json:"root_mode"`
	Passwordless     bool                   `json:"passwordless"`
	RootUndoRan      bool                   `json:"root_undo_ran,omitempty"`
	RootAttempted    bool                   `json:"root_attempted"`
	DeleteLogins     *bool                  `json:"delete_logins,omitempty"`
	Done             []string               `json:"done"`
	Report           api.PurgeResult        `json:"report"`
	Baseline         BaselineChanges        `json:"baseline"`
	Rotate           []string               `json:"rotate"`
	TargetGeneration string                 `json:"target_generation,omitempty"`
}

func (r purgeRemovalReceiptV1) has(phase string) bool { return slices.Contains(r.Done, phase) }
func (r purgeRemovalReceiptV1) readyForGuard() bool {
	return slices.Equal(r.Done, []string{"host", "root", "logins", "baseline", "rotate"})
}
func (r purgeRemovalReceiptV1) complete() bool {
	return slices.Equal(r.Done, []string{"host", "root", "logins", "baseline", "rotate", "guard"})
}

func decodePurgeRemovalV1(raw []byte, h Host) (purgeRemovalReceiptV1, error) {
	var receipt purgeRemovalReceiptV1
	if !api.ExactJSONFields(raw, receipt) || json.Unmarshal(raw, &receipt) != nil || receipt.Version != 2 || !SameEntry(receipt.Original, h) || !ValidHostID(receipt.HostID) || !ValidHostID(receipt.Nonce) {
		return receipt, hostStateRemovalError("the removal receipt has an unknown schema or original host")
	}
	fields, shapeErr := api.JSONObjectFields(raw, api.ExactJSONFieldNames(receipt)...)
	attempted := bytes.TrimSpace(fields["root_attempted"])
	if shapeErr != nil || !bytes.Equal(attempted, []byte("true")) && !bytes.Equal(attempted, []byte("false")) {
		return receipt, hostStateRemovalError("the removal has no explicit original root dispatch history")
	}
	if err := receipt.Source.validate(); err != nil {
		return receipt, err
	}
	if _, err := ParseRootStepMode(string(receipt.RootMode)); err != nil {
		return receipt, err
	}
	phases := []string{"host", "root", "logins", "baseline", "rotate", "guard"}
	if len(receipt.Done) > len(phases) || !slices.Equal(receipt.Done, phases[:len(receipt.Done)]) {
		return receipt, hostStateRemovalError("the removal receipt has no complete ordered stage prefix")
	}
	if receipt.has("host") && (!receipt.Report.HostComplete || receipt.Report.HostID != receipt.HostID || receipt.Report.Nonce != receipt.Nonce || receipt.Report.GuardWitness == nil || !receipt.Report.GuardWitness.Prepared()) {
		return receipt, hostStateRemovalError("the completed host stage has no exact original final witness")
	}
	if receipt.RootUndoRan && (receipt.RootMode != RootRun || !receipt.has("root")) {
		return receipt, hostStateRemovalError("the root undo acknowledgement has no matching actual run phase")
	}
	if receipt.complete() && !receipt.Report.GuardRemoved {
		return receipt, hostStateRemovalError("the completed removal lacks its actual guard acknowledgement")
	}
	if receipt.has("host") {
		if err := validatePurgeHostResultV1(receipt.Report, receipt.HostID, receipt.Nonce); err != nil {
			return receipt, err
		}
	}
	if receipt.TargetGeneration != "" && (!receipt.complete() || !ValidHostID(receipt.TargetGeneration)) {
		return receipt, hostStateRemovalError("the removal receipt has an invalid terminal membership target")
	}
	return receipt, nil
}

func (p *purgeOperationV1) save(ctx context.Context) error {
	if err := p.check(ctx, p.conn); err != nil {
		return err
	}
	ctx = WithHostStateObservation(ctx, p.observation)
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	return withStateFile(ctx, []string{"hosts", HostFileKey(record.Original.Name)}, "removal.json", true, func(raw []byte, exists bool) ([]byte, error) {
		if exists {
			prior, err := decodePurgeRemovalV1(raw, record.Original)
			if err != nil {
				return nil, err
			}
			if prior.HostID != record.HostID || prior.Nonce != record.Nonce || !sameReceiptSource(prior.Source, record.Source) || len(prior.Done) > len(record.Done) {
				return nil, hostStateRemovalError("the removal changed its original attempt or durable stage prefix")
			}
		}
		return json.Marshal(record)
	})
}

func (p *purgeOperationV1) finish(ctx context.Context, phase string) error {
	phases := []string{"host", "root", "logins", "baseline", "rotate", "guard"}
	p.mu.Lock()
	if p.receipt.has(phase) {
		p.mu.Unlock()
		return nil
	}
	if len(p.receipt.Done) >= len(phases) || phases[len(p.receipt.Done)] != phase {
		p.mu.Unlock()
		return grantOperationError()
	}
	p.receipt.Done = append(p.receipt.Done, phase)
	p.mu.Unlock()
	return p.save(ctx)
}

func inspectPurgeRemovalV1(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
	var identity HostStateReceiptIdentity
	if size < 1 || size > 4<<20 || !ValidHostName(name) {
		return identity, hostStateRemovalError("the terminal removal receipt exceeds its fixed bound")
	}
	raw, err := io.ReadAll(io.NewSectionReader(reader, 0, size))
	if err != nil {
		return identity, err
	}
	var envelope struct {
		Original Host `json:"original"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Original.Name != name {
		return identity, hostStateRemovalError("the terminal receipt names another host")
	}
	record, err := decodePurgeRemovalV1(raw, envelope.Original)
	if err != nil {
		return identity, err
	}
	if !record.complete() || record.TargetGeneration == "" {
		return identity, hostStateRemovalError("the original purge has not completed its terminal receipt")
	}
	return HostStateReceiptIdentity{Original: record.Original, HostID: record.HostID, Nonce: record.Nonce, Proof: HostForgetMembershipProof{FromGeneration: record.Source.Generation, TargetGeneration: record.TargetGeneration}}, ctx.Err()
}

func purgeRetryDecisionsV1(record purgeRemovalReceiptV1, force bool, mode RootStepMode, passwordless bool) (purgeRemovalReceiptV1, error) {
	if mode == "" {
		mode = record.RootMode
	}
	if record.RootMode != mode || record.Passwordless != passwordless {
		if record.has("root") || record.RootAttempted {
			return record, hostStateRemovalError("the original root undo was already handled or dispatched; keep its original mode until it is settled")
		}
		record.RootMode, record.Passwordless = mode, passwordless
	}
	if record.Force == force {
		return record, nil
	}
	// This new typed command may explicitly accept work loss after a refusal.
	// It cannot change a completed host stage or any native closure condition.
	if force && !record.Force && !record.has("host") {
		record.Force = true
		return record, nil
	}
	return record, hostStateRemovalError("retry work-loss settings differ from the original removal")
}

func validatePurgeHostResultV1(report api.PurgeResult, hostID, nonce string) error {
	if report.HostID != hostID || report.Nonce != nonce {
		return hostStateRemovalError("the purge report names another original host or operation")
	}
	phases := []string{"precheck", "sessions", "footprints", "roots", "helpers", "host-complete"}
	if len(report.Done) > len(phases) || !slices.Equal(report.Done, phases[:len(report.Done)]) {
		return hostStateRemovalError("the purge report has no complete ordered stage prefix")
	}
	if report.HostComplete {
		witness := report.GuardWitness
		if !slices.Equal(report.Done, phases) || witness == nil || !witness.Prepared() || witness.HostID != hostID || witness.Nonce != nonce || witness.Scope != report.Scope || witness.User != report.User || witness.Arch != report.Arch || report.UID < 0 || witness.Root.UID != uint64(report.UID) {
			return hostStateRemovalError("the completed purge report differs from its original native acknowledgement")
		}
	}
	return nil
}

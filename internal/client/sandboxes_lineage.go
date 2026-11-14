package client

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
)

// A lineage belongs only to this laptop. Structural host answers never
// create one. Schema3 prevents older readers from ignoring its limits.
type sandboxLineage struct {
	Version int64    `json:"version"`
	Parent  string   `json:"parent"`
	Root    string   `json:"root"`
	Allowed []string `json:"allowed_names"`
}

func decodeSandboxLineage(id string, raw json.RawMessage, schema int, sshAgent bool) (*sandboxLineage, bool, error) {
	return decodeSandboxLineageContext(context.Background(), id, raw, schema, sshAgent)
}

func decodeSandboxLineageContext(ctx context.Context, id string, raw json.RawMessage, schema int, sshAgent bool) (*sandboxLineage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if raw == nil {
		return nil, false, nil
	}
	if schema != 3 && schema != 4 || sshAgent {
		return nil, false, badSandboxStateContext(ctx)
	}
	fields, err := sandboxObjectContext(ctx, raw, "version", "parent", "root", "allowed_names")
	if err != nil {
		return nil, false, err
	}
	version, err := sandboxLineageVersion(bytes.TrimSpace(fields["version"]))
	if err != nil {
		return nil, false, err
	}
	if version > 1 {
		return nil, true, nil
	}
	var l sandboxLineage
	l.Version = version
	if json.Unmarshal(fields["parent"], &l.Parent) != nil || json.Unmarshal(fields["root"], &l.Root) != nil ||
		!api.ValidSessionID(l.Parent) || !api.ValidSessionID(l.Root) || l.Parent == id || l.Root == id {
		return nil, false, badSandboxStateContext(ctx)
	}
	if bytes.Equal(bytes.TrimSpace(fields["allowed_names"]), []byte("null")) ||
		json.Unmarshal(fields["allowed_names"], &l.Allowed) != nil || l.Allowed == nil {
		return nil, false, badSandboxStateContext(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	for i, name := range l.Allowed {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || i > 0 && l.Allowed[i-1] >= name {
			return nil, false, badSandboxStateContext(ctx)
		}
	}
	return &l, false, nil
}

// The full decoder and bounded projector share this exact version grammar.
func sandboxLineageVersion(raw []byte) (int64, error) {
	if len(raw) == 0 || len(raw) > 19 {
		return 0, badSandboxState()
	}
	version, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || version <= 0 || strconv.FormatInt(version, 10) != string(raw) {
		return 0, badSandboxState()
	}
	return version, nil
}

func sameSandboxLineage(a, b *sandboxLineage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Version == b.Version && a.Parent == b.Parent && a.Root == b.Root && slices.Equal(a.Allowed, b.Allowed)
}

func localSandboxRoot(p *SandboxPolicy) string {
	if p.FutureLineage || p.FutureDelivery {
		return ""
	}
	if p.lineage != nil {
		return p.lineage.Root
	}
	return p.SessionID
}

func localSandboxNames(p *SandboxPolicy) []string {
	if p == nil || p.FutureLineage || p.FutureDelivery {
		return []string{}
	}
	if p.lineage != nil {
		return slices.Clone(p.lineage.Allowed)
	}
	names := slices.Clone(p.Secrets)
	sort.Strings(names)
	return slices.Compact(names)
}

func checkLocalSandboxNames(p *SandboxPolicy, names []string) error {
	if p != nil && p.FutureDelivery && len(names) != 0 {
		return futureDeliveryError()
	}
	if p != nil && p.FutureLineage && len(names) != 0 {
		return futureLineageError()
	}
	allowed := localSandboxNames(p)
	for _, name := range names {
		_, found := slices.BinarySearch(allowed, name)
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || !found {
			return joinIntentError()
		}
	}
	return nil
}

func cloneSandboxPolicy(p *SandboxPolicy) *SandboxPolicy {
	if p == nil {
		return nil
	}
	q := *p
	q.PolicySources, q.wallIntent, q.lineageRaw = slices.Clone(p.PolicySources), slices.Clone(p.wallIntent), slices.Clone(p.lineageRaw)
	q.deliveryRaw = slices.Clone(p.deliveryRaw)
	q.deliveryReceiptRaw, q.inputOperationRaw = slices.Clone(p.deliveryReceiptRaw), slices.Clone(p.inputOperationRaw)
	q.deliveryReceipt, q.inputOperation = cloneSandboxDeliveryReceipt(p.deliveryReceipt), cloneSandboxInputOperation(p.inputOperation)
	if p.DeliveryIntent != nil {
		v := api.CloneSecretDeliveryIntent(*p.DeliveryIntent)
		q.DeliveryIntent = &v
	}
	q.Secrets, q.Exposed = slices.Clone(p.Secrets), slices.Clone(p.Exposed)
	if p.lineage != nil {
		l := *p.lineage
		l.Allowed = slices.Clone(p.lineage.Allowed)
		q.lineage = &l
	}
	return &q
}

// Ceilings can fall between checks. Every other creation fact stays exact.
func sameSandboxCreation(a, b *SandboxPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return bytes.Equal(a.deliveryRaw, b.deliveryRaw) && sameSandboxDelivery(a, b) && a.FutureLineage == b.FutureLineage && bytes.Equal(a.lineageRaw, b.lineageRaw) && a.SessionID == b.SessionID && a.HostID == b.HostID && a.SSHAgent == b.SSHAgent &&
		a.CreatedAt.Equal(b.CreatedAt) && a.request == b.request &&
		a.CreationWall == b.CreationWall && slices.Equal(a.Exposed, b.Exposed) &&
		bytes.Equal(a.PolicySources, b.PolicySources) && bytes.Equal(a.wallIntent, b.wallIntent) &&
		slices.Equal(localSandboxNames(a), localSandboxNames(b)) && sameSandboxLineage(a.lineage, b.lineage)
}

func joinIntentError() error {
	return &UserError{Msg: "the shared session differs from this laptop's original grant intent",
		Hint: "Start a new request using the original session and its locally allowed secrets.", Code: "join_intent", Kind: KindRefused}
}

// Check the parent inside the same pinned state-file transaction that
// publishes the child. A separate local writer cannot swap its facts.
func validateLineageReservation(f *sandboxFile, child SandboxPolicy) error {
	return validateLineageReservationContext(context.Background(), f, child)
}

func validateLineageReservationContext(ctx context.Context, f *sandboxFile, child SandboxPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if child.FutureDelivery {
		return futureDeliveryError()
	}
	if child.FutureLineage {
		return futureLineageError()
	}
	if child.lineage == nil {
		return nil
	}
	if child.lineage.Version != 1 {
		return futureLineageError()
	}
	raw, ok := f.sessions[child.lineage.Parent]
	if !ok {
		return joinIntentError()
	}
	parent, err := decodeSandboxContext(ctx, child.lineage.Parent, raw, f.schema)
	if err != nil {
		return err
	}
	if parent.FutureDelivery || !sameSandboxDelivery(&parent, &child) || !bytes.Equal(parent.deliveryRaw, child.deliveryRaw) {
		return futureDeliveryError()
	}
	if parent.FutureLineage || child.SSHAgent || child.HostID == "" || child.HostID != parent.HostID ||
		child.Ceiling > parent.Ceiling || child.lineage.Root != localSandboxRoot(&parent) ||
		!slices.Equal(child.lineage.Allowed, localSandboxNames(&parent)) ||
		child.CreationWall != parent.CreationWall || !slices.Equal(child.Exposed, parent.Exposed) ||
		!bytes.Equal(child.PolicySources, parent.PolicySources) || !bytes.Equal(child.wallIntent, parent.wallIntent) {
		return joinIntentError()
	}
	return nil
}

func futureLineageError() error {
	return &UserError{Msg: "this newer session record cannot authorise values or agent access in this client", Hint: "Upgrade the client before sending values or adding a member.", Code: "sandbox_lineage", Kind: KindRefused}
}

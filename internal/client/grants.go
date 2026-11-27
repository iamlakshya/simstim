package client

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

const grantsFileName = "grants.json"

// GrantRecord is one entry in StateDir()/grants.json (isolation spec 14.8.6).
// It stores only host names, host IDs, grant kinds, canonical names and
// counts, never secret values, commands or credential URLs.
type GrantRecord struct {
	HostName string       `json:"host_name"`
	HostID   string       `json:"host_id,omitempty"`
	Kind     OutboundKind `json:"kind"`
	Name     string       `json:"name,omitempty"`
	// Exposure records host exposure at an actual provider-session send.
	// It is history data only and never supplies permission or confirmation.
	Exposure  string     `json:"exposure,omitempty"`
	FirstAt   time.Time  `json:"first_at"`
	LastAt    time.Time  `json:"last_at"`
	Count     int        `json:"count"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	unknown   map[string]json.RawMessage
}

type grantsFile struct {
	Version int           `json:"version"`
	Grants  []GrantRecord `json:"grants"`
	unknown map[string]json.RawMessage
}

func cloneGrantRecord(r GrantRecord) GrantRecord {
	if r.unknown != nil {
		cloned := make(map[string]json.RawMessage, len(r.unknown))
		for name, value := range r.unknown {
			cloned[name] = slices.Clone(value)
		}
		r.unknown = cloned
	}
	if r.RemovedAt != nil {
		t := *r.RemovedAt
		r.RemovedAt = &t
	}
	return r
}

func canonicalGrantName(kind OutboundKind, name string) (string, error) {
	switch kind {
	case OutboundSecret, OutboundSSHKey:
		if !secretNameRE.MatchString(name) {
			return "", &UserError{
				Msg:  "secret grant name is invalid",
				Hint: "Use letters, digits and underscores, as in OPENAI_API_KEY.",
				Code: "bad_secret",
				Kind: KindUsage,
			}
		}
		return name, nil
	case OutboundSSHAgent, OutboundPaste:
		if name != "" {
			return "", &UserError{
				Msg:  string(kind) + " grants take no name",
				Hint: "Leave the grant name empty.",
				Code: "bad_grant",
				Kind: KindUsage,
			}
		}
		return "", nil
	case OutboundRepo:
		return NormalizeRepo(name)
	case OutboundPush, OutboundPublicPush:
		if !canonicalLedgerRepo(name) {
			return "", badRepoError("push ledger identity is not canonical")
		}
		return name, nil
	case OutboundRootRun:
		if !validSandboxReceiptDigest(name) {
			return "", rootStepError("the root-step ledger needs its exact script hash")
		}
		return name, nil
	case OutboundAlertToken:
		if !ValidAlertGrantLabel(name) {
			return "", &UserError{
				Msg:  "alert token grant label is invalid",
				Hint: "Use ntfy or webhook:<hostname>, without a URL path, query or token.",
				Code: "bad_grant",
				Kind: KindUsage,
			}
		}
		return name, nil
	default:
		return "", &UserError{
			Msg:  "grant kind is not tracked in the grant ledger",
			Hint: "Record a recognised credential, repository, push, root-step or paste grant.",
			Code: "bad_grant",
			Kind: KindUsage,
		}
	}
}

func validStoredGrantName(kind OutboundKind, name string) bool {
	if kind == OutboundRepo || kind == OutboundPush || kind == OutboundPublicPush {
		return canonicalLedgerRepo(name)
	}
	canonical, err := canonicalGrantName(kind, name)
	return err == nil && canonical == name
}

func decodeGrantsFile(raw []byte) (grantsFile, error) {
	if len(raw) == 0 {
		return grantsFile{}, badGrantStateError(grantsFileName)
	}
	rootFields, objectErr := api.JSONObjectFields(raw, "version", "grants")
	if objectErr != nil {
		return grantsFile{}, badGrantStateError(grantsFileName)
	}
	var f grantsFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != 1 || f.Grants == nil {
		return grantsFile{}, badGrantStateError(grantsFileName)
	}
	var rows []json.RawMessage
	if json.Unmarshal(rootFields["grants"], &rows) != nil || len(rows) != len(f.Grants) {
		return grantsFile{}, badGrantStateError(grantsFileName)
	}
	f.unknown = unknownLedgerFields(rootFields, "version", "grants")
	for index := range f.Grants {
		r := &f.Grants[index]
		fields, err := api.JSONObjectFields(rows[index], "host_name", "host_id", "kind", "name", "exposure", "first_at", "last_at", "count", "removed_at")
		if err != nil {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
		r.unknown = unknownLedgerFields(fields, "host_name", "host_id", "kind", "name", "exposure", "first_at", "last_at", "count", "removed_at")
		if !ValidHostName(r.HostName) {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
		if r.HostID != "" && !ValidHostID(r.HostID) {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
		if !validStoredGrantName(r.Kind, r.Name) {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
		if r.Count < 1 || r.FirstAt.IsZero() || r.LastAt.IsZero() || r.LastAt.Before(r.FirstAt) {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
		if r.RemovedAt != nil && r.RemovedAt.IsZero() {
			return grantsFile{}, badGrantStateError(grantsFileName)
		}
	}
	return f, nil
}

func encodeGrantsFile(f grantsFile) ([]byte, error) {
	f.Version = 1
	if f.Grants == nil {
		f.Grants = []GrantRecord{}
	}
	slices.SortStableFunc(f.Grants, compareLedgerRecords)
	if ledgerHasUnknownFields(f) {
		return encodeOpaqueGrantsFile(f)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func compareLedgerRecords(a, b GrantRecord) int {
	if c := cmp.Compare(a.HostName, b.HostName); c != 0 {
		return c
	}
	if c := cmp.Compare(a.HostID, b.HostID); c != 0 {
		return c
	}
	if c := cmp.Compare(grantKindRank(a.Kind), grantKindRank(b.Kind)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	aRemoved, bRemoved := 0, 0
	if a.RemovedAt != nil {
		aRemoved = 1
	}
	if b.RemovedAt != nil {
		bRemoved = 1
	}
	if c := cmp.Compare(aRemoved, bRemoved); c != 0 {
		return c
	}
	return a.FirstAt.Compare(b.FirstAt)
}

func checkGrantHostArgs(h Host, hostID string) error {
	if !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	if hostID != "" && !ValidHostID(hostID) {
		return &UserError{
			Msg:  fmt.Sprintf("invalid host ID for %s", h.Name),
			Hint: fmt.Sprintf("Run %s host doctor %s, then try again.", brand.Name, ShellQuote(h.Name)),
			Code: "bad_host_id",
			Kind: KindUsage,
		}
	}
	return nil
}

// RecordGrant adds one grant to StateDir()/grants.json: a name, never a value
// (isolation spec 14.8.6). Every call increments Count for the matching active
// (HostName, HostID, Kind, canonical Name) entry and writes 0600 atomically
// under a cross-platform lock.
func RecordGrant(h Host, hostID string, o Outbound) error {
	return RecordGrantContext(context.Background(), h, hostID, o)
}

// RecordGrantContext is RecordGrant with cancellation for its lock wait
// and persistence. A cancelled caller must send no grant.
func RecordGrantContext(ctx context.Context, h Host, hostID string, o Outbound) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkGrantHostArgs(h, hostID); err != nil {
		return err
	}
	canonName, err := canonicalGrantName(o.Kind, o.Name)
	if err != nil {
		return err
	}
	if err := validateGrantRecordObservation(ctx, h, hostID); err != nil {
		return err
	}
	ctx, endLegacyPermit, err := prepareLegacyLedgerContext(ctx, h, hostID, o)
	if err != nil {
		return err
	}
	defer endLegacyPermit()
	if err := validateLegacyRecordArguments(ctx, h, hostID, o); err != nil {
		return err
	}
	if o.Kind == OutboundSSHAgent {
		return recordAgentGrantFD(ctx, h, hostID)
	}
	// Validate the normalised identity without treating it as another raw
	// URL. In particular a remaining .git suffix is part of this identity.
	if o.Kind == OutboundRepo && !canonicalLedgerRepo(canonName) {
		return badRepoError("repository origin has no valid canonical identity")
	}
	exposure := sessionMachineGrantExposureV1(ctx, h, o.Kind)
	return withStateFile(ctx, nil, grantsFileName, true, func(raw []byte, exists bool) ([]byte, error) {
		f := grantsFile{Version: 1, Grants: []GrantRecord{}}
		if exists {
			var derr error
			f, derr = decodeGrantsFile(raw)
			if derr != nil {
				return nil, derr
			}
		}
		now := nowUTC()
		found := false
		for i := range f.Grants {
			r := &f.Grants[i]
			if r.RemovedAt == nil && r.HostName == h.Name && r.HostID == hostID && r.Kind == o.Kind && r.Name == canonName && compatibleGrantExposureV1(r.Exposure, exposure) {
				if r.Count < 0 || r.Count == int(^uint(0)>>1) {
					return nil, grantCountError()
				}
				r.Count++
				r.Exposure = mergeGrantExposureV1(r.Exposure, exposure)
				if now.After(r.LastAt) {
					r.LastAt = now
				}
				found = true
				break
			}
		}
		if !found {
			f.Grants = append(f.Grants, GrantRecord{
				HostName: h.Name,
				HostID:   hostID,
				Kind:     o.Kind,
				Name:     canonName,
				Exposure: exposure,
				FirstAt:  now,
				LastAt:   now,
				Count:    1,
			})
		}
		return encodeGrantsFile(f)
	})
}

// LoadGrants reads all records in StateDir()/grants.json, including historical
// entries whose RemovedAt timestamp is set. It returns nil, nil when
// grants.json does not exist, and fails closed when the file is damaged or not
// a regular file.
func LoadGrants() ([]GrantRecord, error) {
	return LoadGrantsContext(context.Background())
}

// LoadGrantsContext is LoadGrants with cancellation for a running command.
func LoadGrantsContext(ctx context.Context) ([]GrantRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []GrantRecord
	err := withStateFile(ctx, nil, grantsFileName, false, func(raw []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, nil
		}
		f, err := decodeGrantsFile(raw)
		if err != nil {
			return nil, err
		}
		out = make([]GrantRecord, len(f.Grants))
		for i, r := range f.Grants {
			out[i] = cloneGrantRecord(r)
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GrantsForHost returns active (RemovedAt == nil) records for h matching
// hostID (or all active records for h.Name when hostID == ""), sorted by kind
// and canonical name.
func GrantsForHost(h Host, hostID string) ([]GrantRecord, error) {
	return GrantsForHostContext(context.Background(), h, hostID)
}

// GrantsForHostContext is GrantsForHost with cancellation for ledger reads.
func GrantsForHostContext(ctx context.Context, h Host, hostID string) ([]GrantRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkGrantHostArgs(h, hostID); err != nil {
		return nil, err
	}
	all, err := LoadGrantsContext(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		kind OutboundKind
		name string
	}
	merged := map[key]GrantRecord{}
	for _, r := range all {
		if r.RemovedAt != nil || r.HostName != h.Name {
			continue
		}
		if hostID != "" && r.HostID != hostID {
			continue
		}
		k := key{kind: r.Kind, name: r.Name}
		prev, ok := merged[k]
		if !ok {
			merged[k] = cloneGrantRecord(r)
			continue
		}
		if r.Count > int(^uint(0)>>1)-prev.Count {
			return nil, grantCountError()
		}
		prev.Count += r.Count
		prev.Exposure = mergeGrantExposureV1(prev.Exposure, r.Exposure)
		if r.FirstAt.Before(prev.FirstAt) {
			prev.FirstAt = r.FirstAt
		}
		if r.LastAt.After(prev.LastAt) {
			prev.LastAt = r.LastAt
		}
		if prev.HostID != r.HostID {
			prev.HostID = hostID
		}
		merged[k] = prev
	}
	out := make([]GrantRecord, 0, len(merged))
	for _, r := range merged {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b GrantRecord) int {
		if c := cmp.Compare(grantKindRank(a.Kind), grantKindRank(b.Kind)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out, nil
}

func grantCountError() error {
	return &UserError{Msg: "the saved grant count is too large to update safely",
		Hint: "Check this computer's grant ledger before trying again.", Code: "grant_store"}
}

// RotateSecrets returns the sorted, deduplicated names of active rotatable
// grants (secret, ssh-key and alert-token) recorded for h.Name across all host
// IDs, as well as any active rotatable grants matching h's current HostID in
// tiers.toml when present.
func RotateSecrets(h Host) ([]string, error) {
	return RotateSecretsContext(context.Background(), h)
}

// RotateSecretsContext is RotateSecrets with cancellation for ledger reads.
func RotateSecretsContext(ctx context.Context, h Host) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidHostName(h.Name) {
		return nil, badHostNameError(h.Name)
	}
	var tierHostID string
	ts, err := readTiersFile()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		tierHostID = ts.entries[h.Name].HostID
	}
	all, err := LoadGrantsContext(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range all {
		if r.RemovedAt != nil || !RotatableGrantKind(r.Kind) {
			continue
		}
		if r.HostName != h.Name && (tierHostID == "" || r.HostID != tierHostID) {
			continue
		}
		if !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// MarkGrantsRemoved stamps active grant records for h.Name with RemovedAt so
// audit history stays in grants.json while a later host of the same name
// starts clean.
func MarkGrantsRemoved(h Host) error {
	return MarkGrantsRemovedContext(context.Background(), h)
}

// MarkGrantsRemovedContext is MarkGrantsRemoved with cancellation for its
// lock wait and write after a host removal succeeds.
func MarkGrantsRemovedContext(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	return WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		return withStateFile(verified, nil, grantsFileName, true, func(raw []byte, exists bool) ([]byte, error) {
			if !exists {
				return nil, errStateNoChange
			}
			f, err := decodeGrantsFile(raw)
			if err != nil {
				return nil, err
			}
			now := nowUTC()
			changed := false
			for i := range f.Grants {
				if f.Grants[i].RemovedAt == nil && f.Grants[i].HostName == h.Name {
					at := now
					f.Grants[i].RemovedAt = &at
					changed = true
				}
			}
			if !changed {
				return nil, errStateNoChange
			}
			return encodeGrantsFile(f)
		})
	})
}

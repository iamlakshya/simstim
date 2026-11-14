package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// SandboxPolicy is this laptop's policy for one session. It is never sent
// to the host. It holds grant names and settings, never fetched credential values.
type SandboxPolicy struct {
	// PolicySources is an opaque immutable local source pin. Tier and
	// exposure admission do not interpret its settings or source names.
	PolicySources json.RawMessage
	// FutureLineage is a newer local format. It never grants send authority.
	FutureLineage bool
	// DeliveryIntent is original local metadata. FutureDelivery grants no authority.
	DeliveryIntent *api.SecretDeliveryIntent
	FutureDelivery bool
	SessionID      string
	HostID         string
	Ceiling        Tier
	SSHAgent       bool
	Secrets        []string
	CreatedAt      time.Time
	// CreationWall and Exposed are immutable local creation facts. A host
	// reply never supplies them. Schema1 creations decode as unwalled.
	CreationWall          string
	Exposed               []string
	request               string
	wallIntent            json.RawMessage
	lineage               *sandboxLineage
	lineageRaw            json.RawMessage
	deliveryRaw           json.RawMessage
	deliveryReceipt       *sandboxDeliveryReceipt
	inputOperation        *sandboxInputOperation
	deliveryReceiptRaw    json.RawMessage
	inputOperationRaw     json.RawMessage
	futureDeliveryReceipt bool
	futureInputOperation  bool
}

type savedSandbox struct {
	PolicySources   json.RawMessage `json:"policy_sources,omitempty"`
	SessionID       string          `json:"session_id"`
	HostID          string          `json:"host_id,omitempty"`
	Ceiling         string          `json:"ceiling"`
	SSHAgent        bool            `json:"ssh_agent,omitempty"`
	Secrets         []string        `json:"secrets,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	Request         string          `json:"request"`
	WallIntent      json.RawMessage `json:"wall_intent,omitempty"`
	Lineage         json.RawMessage `json:"lineage,omitempty"`
	DeliveryIntent  json.RawMessage `json:"delivery_intent,omitempty"`
	DeliveryReceipt json.RawMessage `json:"delivery_receipt,omitempty"`
	InputOperation  json.RawMessage `json:"input_operation,omitempty"`
}

type sandboxFile struct {
	schema   int
	root     map[string]json.RawMessage
	sessions map[string]json.RawMessage
}

// LoadSandboxPolicy reads the saved ceiling and agent intent for id.
// A session saved before this client has no entry. An unreadable or
// malformed file is an error and must never raise a request's ceiling.
func LoadSandboxPolicy(ctx context.Context, h Host, id string) (*SandboxPolicy, error) {
	if err := checkSessionID(h.Name, id); err != nil {
		return nil, err
	}
	var found *SandboxPolicy
	err := withSandboxFile(ctx, h, false, func(f *sandboxFile) error {
		if raw, ok := f.sessions[id]; ok {
			p, err := decodeSandboxContext(ctx, id, raw, f.schema)
			if err != nil {
				return err
			}
			found = &p
		}
		return nil
	})
	return found, err
}

// SandboxPolicies lists this laptop's known sessions on h, sorted by ID.
// It includes earlier host identities so a tier change can explain them.
func SandboxPolicies(ctx context.Context, h Host) ([]SandboxPolicy, error) {
	var out []SandboxPolicy
	err := withSandboxFile(ctx, h, false, func(f *sandboxFile) error {
		for id, raw := range f.sessions {
			if err := ctx.Err(); err != nil {
				return err
			}
			p, err := decodeSandboxContext(ctx, id, raw, f.schema)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, err
}

// EffectiveSessionTier takes the lower of the host's current tier and
// the ceiling that this laptop saved, and this request. Host replies cannot raise it.
func EffectiveSessionTier(ctx context.Context, h Host, id, hostID string) (Tier, *SandboxPolicy, error) {
	tier, savedID, err := grantHostTier(ctx, h)
	if err != nil {
		return TierUnknown, nil, err
	}
	if savedID != "" && savedID != hostID {
		return TierUnknown, nil, grantIdentityError(h)
	}
	p, err := LoadSandboxPolicy(ctx, h, id)
	if err != nil {
		return TierUnknown, nil, err
	}
	if p != nil {
		if p.HostID != "" && p.HostID != hostID {
			return TierUnknown, nil, grantIdentityError(h)
		}
		tier = min(tier, p.Ceiling)
	}
	tier = min(tier, requestCeiling(ctx, tier))
	return tier, p, nil
}

func decodeSandbox(id string, raw json.RawMessage, schema int) (SandboxPolicy, error) {
	return decodeSandboxContext(context.Background(), id, raw, schema)
}

func decodeSandboxContext(ctx context.Context, id string, raw json.RawMessage, schema int) (SandboxPolicy, error) {
	if err := ctx.Err(); err != nil {
		return SandboxPolicy{}, err
	}
	fields, err := sandboxObjectContext(ctx, raw, "session_id", "host_id", "ceiling", "ssh_agent", "secrets", "created_at", "request", "wall_intent", "policy_sources", "lineage", "delivery_intent", "delivery_receipt", "input_operation")
	if err != nil {
		return SandboxPolicy{}, err
	}
	var p savedSandbox
	typedErr := json.Unmarshal(raw, &p)
	if err := ctx.Err(); err != nil {
		return SandboxPolicy{}, err
	}
	if typedErr != nil || p.SessionID != id || !api.ValidSessionID(id) {
		return SandboxPolicy{}, badSandboxStateContext(ctx)
	}
	tier, err := ParseTier(p.Ceiling)
	if err != nil || p.HostID != "" && !ValidHostID(p.HostID) || p.CreatedAt.IsZero() || p.Request == "" {
		return SandboxPolicy{}, badSandboxStateContext(ctx)
	}
	for _, name := range p.Secrets {
		if err := ctx.Err(); err != nil {
			return SandboxPolicy{}, err
		}
		if !secretNameRE.MatchString(name) {
			return SandboxPolicy{}, badSandboxStateContext(ctx)
		}
	}
	intent := savedCreationIntent{Level: "none"}
	if schema == 1 {
		if _, present := fields["wall_intent"]; present {
			return SandboxPolicy{}, badSandboxStateContext(ctx)
		}
	} else if schema == 2 || schema == 3 || schema == 4 {
		intent, err = decodeCreationIntentContext(ctx, p.WallIntent)
		if err != nil {
			return SandboxPolicy{}, err
		}
	} else {
		return SandboxPolicy{}, badSandboxStateContext(ctx)
	}
	if p.SSHAgent && intent.Level != "none" {
		return SandboxPolicy{}, badSandboxStateContext(ctx)
	}
	lineage, future, err := decodeSandboxLineageContext(ctx, id, fields["lineage"], schema, p.SSHAgent)
	if err != nil {
		return SandboxPolicy{}, err
	}
	if lineage != nil && (p.SSHAgent || p.HostID == "" || intent.Level == "none") {
		return SandboxPolicy{}, badSandboxStateContext(ctx)
	}
	if lineage != nil {
		for i, name := range p.Secrets {
			if err := ctx.Err(); err != nil {
				return SandboxPolicy{}, err
			}
			if _, ok := slices.BinarySearch(lineage.Allowed, name); !ok || i > 0 && p.Secrets[i-1] >= name {
				return SandboxPolicy{}, badSandboxStateContext(ctx)
			}
		}
	}
	out := SandboxPolicy{deliveryReceiptRaw: slices.Clone(p.DeliveryReceipt), inputOperationRaw: slices.Clone(p.InputOperation), deliveryRaw: slices.Clone(fields["delivery_intent"]), FutureLineage: future, lineageRaw: slices.Clone(fields["lineage"]), PolicySources: slices.Clone(p.PolicySources), SessionID: id, HostID: p.HostID, Ceiling: tier, SSHAgent: p.SSHAgent,
		Secrets: slices.Clone(p.Secrets), CreatedAt: p.CreatedAt, request: p.Request,
		CreationWall: intent.Level, Exposed: slices.Clone(intent.Exposed), wallIntent: slices.Clone(p.WallIntent), lineage: lineage}
	if fields["delivery_intent"] != nil {
		if err := decodeSandboxDeliveryContext(ctx, raw, schema, &out); err != nil {
			return SandboxPolicy{}, badSandboxStateContext(ctx)
		}
	}
	if err := decodeSandboxReceiptsContext(ctx, raw, schema, &out); err != nil {
		return SandboxPolicy{}, err
	}
	if err := ctx.Err(); err != nil {
		return SandboxPolicy{}, err
	}
	return out, nil
}

// reserveSandbox is idempotent only for the same logical request. A
// collision can neither replace another request nor raise its ceiling.
func reserveSandbox(ctx context.Context, h Host, p SandboxPolicy) error {
	p = *cloneSandboxPolicy(&p)
	var err error
	p, err = normalizeCreationIntent(p)
	if err != nil {
		return err
	}
	ctx, endLegacyPermit, err := prepareLegacyNoneContext(ctx, h, p)
	if err != nil {
		return err
	}
	defer endLegacyPermit()
	return withSandboxFile(ctx, h, true, func(f *sandboxFile) error {
		if err := checkLegacyNoneSchema(ctx, f.schema); err != nil {
			return err
		}
		if err := validateLineageReservationContext(ctx, f, p); err != nil {
			return err
		}
		if p.lineage != nil && f.schema < 3 {
			f.schema, f.root["schema"] = 3, json.RawMessage("3")
		}
		if p.FutureDelivery {
			return futureDeliveryError()
		}
		if p.DeliveryIntent != nil && f.schema != 4 {
			return &UserError{Msg: "the bound delivery schema barrier has not been committed", Hint: "Prepare the original sealed delivery plan before reading values.", Code: "grant_scope", Kind: KindRefused}
		}
		if p.deliveryReceipt != nil || p.inputOperation != nil || p.deliveryReceiptRaw != nil || p.inputOperationRaw != nil || p.futureDeliveryReceipt || p.futureInputOperation {
			return sandboxReceiptError()
		}
		var fields map[string]json.RawMessage
		if old, ok := f.sessions[p.SessionID]; ok {
			before, err := decodeSandboxContext(ctx, p.SessionID, old, f.schema)
			if err != nil {
				return err
			}
			if before.request != p.request || before.HostID != p.HostID {
				return errSandboxIDTaken
			}
			if before.CreationWall != p.CreationWall || !slices.Equal(before.Exposed, p.Exposed) {
				return &UserError{Msg: "this retry changes the session's immutable wall or exposure intent",
					Hint: "Start a new request after resolving its wall and secret exposure.", Code: "wall_intent", Kind: KindRefused}
			}
			if !bytes.Equal(before.PolicySources, p.PolicySources) {
				return policySourceStateError()
			}
			if !slices.Equal(before.Secrets, p.Secrets) {
				return grantOperationError()
			}
			if !sameSandboxDelivery(&before, &p) {
				return grantOperationError()
			}
			if !sameSandboxLineage(before.lineage, p.lineage) {
				return joinIntentError()
			}
			p.Ceiling = min(before.Ceiling, p.Ceiling)
			p.CreatedAt = before.CreatedAt
			if err := json.Unmarshal(old, &fields); err != nil {
				return badSandboxState()
			}
		}
		intent, err := json.Marshal(savedCreationIntent{Level: p.CreationWall, Exposed: p.Exposed})
		if err != nil {
			return err
		}
		b, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(),
			SSHAgent: p.SSHAgent, Secrets: p.Secrets, CreatedAt: p.CreatedAt, Request: p.request, WallIntent: intent})
		if err != nil {
			return err
		}
		var known map[string]json.RawMessage
		if err := json.Unmarshal(b, &known); err != nil {
			return err
		}
		if p.wallIntent != nil {
			known["wall_intent"] = slices.Clone(p.wallIntent)
		}
		if p.DeliveryIntent != nil {
			known["delivery_intent"], err = json.Marshal(p.DeliveryIntent)
			if err != nil {
				return err
			}
			if p.deliveryRaw != nil {
				known["delivery_intent"] = slices.Clone(p.deliveryRaw)
			}
		}
		if p.lineage != nil {
			known["lineage"], err = json.Marshal(p.lineage)
			if err != nil {
				return err
			}
		}
		if p.PolicySources != nil {
			if !json.Valid(p.PolicySources) {
				return policySourceStateError()
			}
			known["policy_sources"] = p.PolicySources
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		for key, value := range known {
			if (key == "wall_intent" || key == "policy_sources" || key == "lineage" || key == "delivery_intent") && fields[key] != nil {
				continue // Creation bytes and future nested metadata never change on retry.
			}
			fields[key] = value
		}
		// Optional known fields must not retain stale values from a retry.
		for _, key := range []string{"host_id", "ssh_agent", "secrets"} {
			if _, ok := known[key]; !ok {
				delete(fields, key)
			}
		}
		f.sessions[p.SessionID], err = marshalSandboxObject(fields)
		if err != nil {
			return err
		}
		decoded, err := decodeSandboxContext(ctx, p.SessionID, f.sessions[p.SessionID], f.schema)
		if err != nil {
			return err
		}
		if !sameSandboxDelivery(&decoded, &p) {
			return grantOperationError()
		}
		return nil
	})
}

var errSandboxIDTaken = errors.New("a different request already reserved the session ID")

func emptySandboxFile(h Host) *sandboxFile {
	host, _ := json.Marshal(h.Name)
	return &sandboxFile{schema: 2, root: map[string]json.RawMessage{"schema": []byte("2"), "host": host}, sessions: map[string]json.RawMessage{}}
}

func decodeSandboxFile(b []byte, h Host) (*sandboxFile, error) {
	return decodeSandboxFileContext(context.Background(), b, h)
}

func decodeSandboxFileContext(ctx context.Context, b []byte, h Host) (*sandboxFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f := &sandboxFile{}
	var err error
	f.root, err = sandboxObjectContext(ctx, b, "schema", "host", "sessions")
	if err != nil {
		return nil, err
	}
	var schema int
	var host string
	if json.Unmarshal(f.root["schema"], &schema) != nil || (schema != 1 && schema != 2 && schema != 3 && schema != 4) || json.Unmarshal(f.root["host"], &host) != nil || host != h.Name {
		return nil, badSandboxStateContext(ctx)
	}
	f.schema = schema
	f.sessions, err = sandboxObjectContext(ctx, f.root["sessions"])
	if err != nil {
		return nil, err
	}
	for id, raw := range f.sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := decodeSandboxContext(ctx, id, raw, f.schema); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

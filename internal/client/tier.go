package client

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Tier is how far the user trusts a host. The host never learns it
// (docs/contributing/isolation-spec.md 14.8).
type Tier int

// The tiers, from least to most trusted.
const (
	TierUnknown Tier = iota
	TierShared
	TierThrowaway
	TierOwn
)

var tierNames = [...]string{"", "shared", "throwaway", "own"}

// String returns the tier's lower-case name, or "" for TierUnknown.
func (t Tier) String() string {
	if t < TierShared || int(t) >= len(tierNames) {
		return ""
	}
	return tierNames[t]
}

// Valid reports whether t is one of shared, throwaway or own.
func (t Tier) Valid() bool {
	return t >= TierShared && t <= TierOwn
}

// ParseTier parses own, throwaway or shared. Any other value is a usage
// error with code bad_tier.
func ParseTier(s string) (Tier, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "own":
		return TierOwn, nil
	case "throwaway":
		return TierThrowaway, nil
	case "shared":
		return TierShared, nil
	default:
		return TierUnknown, &UserError{
			Msg:  fmt.Sprintf("%q is not a host tier", CleanLine(s)),
			Hint: "Use own, throwaway or shared.",
			Code: "bad_tier",
			Kind: KindUsage,
		}
	}
}

// ProviderTier returns the tier for a host that host create made: shared
// for hosted providers such as e2b, and own for local ones (lima, docker).
func ProviderTier(provider string) Tier {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "lima", "docker":
		return TierOwn
	case "e2b":
		return TierShared
	default:
		return TierShared
	}
}

// ValidHostID reports whether id is 32 lower-case hex characters.
func ValidHostID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// TierEntry is one host's entry in tiers.toml.
type TierEntry struct {
	Name      string
	Tier      Tier
	HostID    string
	Key       string
	SetAt     time.Time
	IDChanged bool
	// NeedsAnswer keeps an observed host at shared until the user chooses.
	NeedsAnswer bool
}

// Tiers holds the entries of tiers.toml.
type Tiers struct {
	Migrated time.Time
	entries  map[string]TierEntry
}

// NewTiers builds a Tiers from entries in memory, for tests and demo mode.
func NewTiers(entries map[string]TierEntry) Tiers {
	out := make(map[string]TierEntry, len(entries))
	for name, e := range entries {
		e.Name = name
		out[name] = e
	}
	return Tiers{Migrated: time.Unix(1, 0).UTC(), entries: out}
}

// Of returns a host's tier. A host with no entry counts as shared
// (isolation spec 14.6.1).
func (t Tiers) Of(name string) Tier {
	if e, ok := t.entries[name]; ok && e.Tier.Valid() && !e.NeedsAnswer {
		return e.Tier
	}
	return TierShared
}

// Has reports whether tiers.toml has an entry for name.
func (t Tiers) Has(name string) bool {
	_, ok := t.entries[name]
	return ok
}

// NeedsAnswer reports whether the user has yet to choose this host's tier.
func (t Tiers) NeedsAnswer(name string) bool {
	e, ok := t.entries[name]
	return !ok || e.NeedsAnswer
}

// IDChanged reports whether name answered with a different host ID since
// its tier was last set or rekeyed.
func (t Tiers) IDChanged(name string) bool {
	return t.entries[name].IDChanged
}

// Entry returns name's entry in tiers.toml, if present.
func (t Tiers) Entry(name string) (TierEntry, bool) {
	e, ok := t.entries[name]
	return e, ok
}

// LoadTiers reads tiers.toml, and migrates once from hosts and their pinned
// host keys when tiers.toml does not exist (isolation spec 14.6.1).
// LoadTiers is only for context-free call stacks. Any caller with a
// context must use LoadTiersContext instead.
func LoadTiers(hosts []Host) (Tiers, error) {
	return LoadTiersContext(context.Background(), hosts)
}

// LoadTiersContext is LoadTiers with cancellation for reads and lock waits.
func LoadTiersContext(ctx context.Context, hosts []Host) (Tiers, error) {
	return loadTiers(ctx, hosts)
}

// SetTier saves a host's tier, its host ID and its key fingerprint in
// tiers.toml. Empty hostID or key keeps the value already stored.
func SetTier(name string, t Tier, hostID, key string) error {
	return SetTierContext(context.Background(), name, t, hostID, key)
}

// SetTierContext is SetTier with a context for lock waits.
func SetTierContext(ctx context.Context, name string, t Tier, hostID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(name) {
		return badHostNameError(name)
	}
	if !t.Valid() {
		return &UserError{
			Msg:  fmt.Sprintf("%q is not a host tier", CleanLine(t.String())),
			Hint: "Use own, throwaway or shared.",
			Code: "bad_tier",
			Kind: KindUsage,
		}
	}
	if hostID != "" && !ValidHostID(hostID) {
		return &UserError{Msg: "invalid host ID for " + name,
			Hint: "Use 32 lower-case hex characters for the host ID.", Code: "bad_value", Kind: KindUsage}
	}
	if err := checkFingerprint(key); err != nil {
		return err
	}
	ctx, unlock, err := LockHost(ctx, name, "host set")
	if err != nil {
		return err
	}
	defer unlock()
	h, err := FindHost(name)
	if err != nil {
		return err
	}
	if err := ensureTiersMigrated(ctx); err != nil {
		return err
	}
	var pinnedKey string
	if key == "" {
		if p, err := PinOf(name); err == nil {
			pinnedKey = p.Fingerprint
		}
	}
	return mutateSavedHostTiers(ctx, h, "host set", func(ts *Tiers) error {
		e := ts.entries[name]
		e.Name = name
		e.Tier = t
		if hostID != "" {
			e.HostID = hostID
		}
		if key != "" {
			e.Key = key
		} else if e.Key == "" {
			e.Key = pinnedKey
		}
		e.IDChanged = false
		e.NeedsAnswer = false
		e.SetAt = nowUTC()
		ts.entries[name] = e
		return nil
	})
}

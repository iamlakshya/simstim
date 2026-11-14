package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/brand"
)

// WarnFunc receives a client warning with its code, message and Try: hint.
type WarnFunc func(code, msg, hint string)

type warnKey struct{}

// WithWarnings returns a context that delivers client warnings, such as a
// changed host ID, to fn.
func WithWarnings(ctx context.Context, fn WarnFunc) context.Context {
	return context.WithValue(ctx, warnKey{}, fn)
}

func warn(ctx context.Context, code, msg, hint string) {
	DebugLog().Warn(msg, "code", code)
	if fn, _ := ctx.Value(warnKey{}).(WarnFunc); fn != nil {
		fn(code, msg, hint)
	}
}

// recordedTierKey returns the key fingerprint stored for name in tiers.toml,
// when tiers.toml exists. A malformed tiers.toml returns an error.
func recordedTierKey(name string) (string, error) {
	ts, err := readTiersFile()
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ts.entries[name].Key, nil
}

// checkTierKeyPin checks that a saved host whose key is recorded in
// tiers.toml still has a matching pin file (isolation spec 14.7.1).
func checkTierKeyPin(ctx context.Context, name string) error {
	if !ValidHostName(name) {
		return nil
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == name }) {
		return nil
	}
	ts, err := LoadTiersContext(ctx, hosts)
	if err != nil {
		return err
	}
	wantKey := ts.entries[name].Key
	if wantKey == "" {
		return nil
	}
	keys, err := readPin(name)
	if errors.Is(err, os.ErrNotExist) {
		return missingPinError(name, wantKey)
	}
	if err != nil {
		if isForeign(err) {
			return err
		}
		return &UserError{
			Msg:  fmt.Sprintf("%s's pinned host key cannot be read, so the client does not connect: %v", name, err),
			Hint: rekeyHint(name),
			Code: "host_key_unreadable",
			Kind: KindRefused,
		}
	}
	if _, ok := withFingerprint(keys, wantKey); !ok {
		return mismatchedTierKeyError(name, wantKey, pinOf(keys, "").Fingerprint)
	}
	return nil
}

func missingPinError(name, wantKey string) error {
	return &UserError{
		Msg:  fmt.Sprintf("%s's pinned host key (%s) is missing, so the client does not connect", name, wantKey),
		Hint: rekeyHint(name),
		Code: "host_key_unreadable",
		Kind: KindRefused,
	}
}

func mismatchedTierKeyError(name, wantKey, gotKey string) error {
	return &UserError{
		Msg:  fmt.Sprintf("%s's pinned host key file holds %s, not %s from tiers.toml, so the client does not connect", name, gotKey, wantKey),
		Hint: rekeyHint(name),
		Code: "host_key_unreadable",
		Kind: KindRefused,
	}
}

// recordPinnedKey copies a newly pinned key fingerprint into tiers.toml for a
// saved host that had none yet.
func recordPinnedKey(ctx context.Context, h Host, fp string) error {
	name := h.Name
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(name) || fp == "" {
		return nil
	}
	ctx, unlock, err := LockHost(ctx, name, "host test")
	if err != nil {
		return err
	}
	defer unlock()
	if err := ensureTiersMigrated(ctx); err != nil {
		return err
	}
	return mutateSavedHostTiers(ctx, h, "host test", func(ts *Tiers) error {
		e, ok := ts.entries[name]
		if !ok {
			e = TierEntry{Name: name, Tier: TierShared, SetAt: nowUTC(), NeedsAnswer: true}
		}
		e.Key = fp
		ts.entries[name] = e
		return nil
	})
}

// recordRekey sets name's tier to shared and updates its key fingerprint in
// tiers.toml after host rekey replaces the pin (isolation spec 14.7.1).
func recordRekey(ctx context.Context, h Host, newKey string) error {
	name := h.Name
	return mutateSavedHostTiers(ctx, h, "host rekey", func(ts *Tiers) error {
		e, had := ts.entries[name]
		if !had {
			e.NeedsAnswer = true
		}
		e.Name = name
		e.Tier = TierShared
		e.Key = newKey
		e.IDChanged = false
		e.SetAt = nowUTC()
		ts.entries[name] = e
		return nil
	})
}

// noteHostID records gotID on first sight for a saved host, or downgrades its
// tier to shared and emits a warning when gotID differs from the stored ID
// (isolation spec 14.7.2).
func noteHostID(ctx context.Context, h Host, gotID string) (changed bool, err error) {
	name := h.Name
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !ValidHostName(name) {
		return false, nil
	}
	if gotID != "" && !ValidHostID(gotID) {
		return false, &UserError{Msg: "the helper on " + name + " sent an invalid host ID",
			Hint: brand.Name + " host doctor " + name, Code: "protocol"}
	}
	saved, err := SavedEntry(name)
	if err != nil {
		return false, err
	}
	if saved == nil {
		return false, nil
	}
	if !SameEntry(*saved, h) {
		return false, (HostChange{Kind: "connect", Was: &h, Now: &h}).changed(saved)
	}
	// An unchanged hello changes no policy. Ordinary reads can proceed
	// while a host command holds its lock at a question.
	if ts, err := readTiersFile(); err == nil && ts.entries[name].HostID != "" && ts.entries[name].HostID == gotID {
		return false, nil
	}
	ctx, unlock, err := LockHost(ctx, name, "connect")
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ensureTiersMigrated(ctx); err != nil {
		return false, err
	}
	var pinnedKey string
	if p, err := PinOf(name); err == nil {
		pinnedKey = p.Fingerprint
	}
	err = mutateSavedHostTiers(ctx, h, "connect", func(ts *Tiers) error {
		e, ok := ts.entries[name]
		if !ok {
			e = TierEntry{Name: name, Tier: TierShared, SetAt: nowUTC(), NeedsAnswer: true}
		}
		switch {
		case e.HostID == "" && gotID == "":
			return nil
		case e.HostID == "":
			e.HostID = gotID
			if e.Key == "" {
				e.Key = pinnedKey
			}
			ts.entries[name] = e
			return nil
		case gotID == e.HostID:
			return nil
		default:
			changed = true
			e.Tier = TierShared
			e.HostID = gotID
			e.IDChanged = true
			e.SetAt = nowUTC()
			ts.entries[name] = e
			return nil
		}
	})
	if err != nil {
		return false, err
	}
	if changed {
		warn(ctx, "host_id_changed",
			name+" answers with a different host ID, so its tier is now shared.",
			fmt.Sprintf("If it is the same machine: %s host set %s tier=own", brand.Name, name))
	}
	return changed, nil
}

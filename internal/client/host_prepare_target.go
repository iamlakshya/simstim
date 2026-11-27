package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/brand"
)

type hostPrepareCommitCaptureKey struct{}
type hostPrepareRecordCheckKey struct{}

// HostPrepareTargetV1 is an opaque writer- or saved-source-derived target for
// host sandbox preparation. It cannot be constructed from a raw Host value,
// DTO, nonce or FindHost lookup, and never promotes a registration connection
// or adopts a removed-and-readded host.
type HostPrepareTargetV1 struct {
	machineOwner     *SessionMachineOwnerV1
	writerOrigin     bool
	fromRegistration bool
	host             Host
	source           hostStateSource
	stateDir         string
	hostsPath        string
	tier             Tier
	tierHasEntry     bool
	tierNeedsAnswer  bool
	tierHasKey       bool
	hostID           string
	key              string
}

type hostPrepareCommitCapture struct {
	name   string
	target *HostPrepareTargetV1
}

// OpenHostPrepareTargetV1 opens an existing original saved host before probe
// under the canonical membership lock. The returned target binds the exact
// saved entry, membership generation, native root, tier, host ID and pinned
// host key so later phases fail closed on removal, re-add ABA or mutation.
func OpenHostPrepareTargetV1(ctx context.Context, name string) (*HostPrepareTargetV1, error) {
	if ctx == nil {
		return nil, hostStateRemovalError("opening a preparation target needs an active context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := refuseNestedHostPrepareScope(ctx); err != nil {
		return nil, err
	}
	if !ValidHostName(name) {
		return nil, badHostNameError(name)
	}
	if err := ensureTiersMigrated(ctx); err != nil {
		return nil, err
	}
	var target *HostPrepareTargetV1
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == name })
		if i < 0 {
			names := make([]string, len(hosts))
			for j, h := range hosts {
				names[j] = h.Name
			}
			return &UnknownNameError{
				UserError: &UserError{Msg: fmt.Sprintf("no host named %q", name), Hint: fmt.Sprintf("%s host add %s", brand.Name, name), Code: "host_not_found", Kind: KindNotFound},
				Name:      name, Known: names,
			}
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		unlockTiers, err := lockTiersFile(locked)
		if err != nil {
			return err
		}
		defer unlockTiers()
		built, err := captureHostPrepareTargetLocked(locked, hosts[i], source, false)
		if err != nil {
			return err
		}
		target = built
		return nil
	})
	if err != nil {
		return nil, err
	}
	return target, nil
}

// UpdateHostsForPreparation runs the canonical UpdateHosts writer CAS and
// returns a HostPrepareTargetV1 ONLY from the real successful post-write
// commit callback while the canonical hosts.toml membership lock is still held.
// Any forget, CAS mismatch, rollback, cancellation or write error returns nil.
func UpdateHostsForPreparation(ctx context.Context, c HostChange) (*HostPrepareTargetV1, error) {
	if ctx == nil {
		return nil, hostStateRemovalError("saving a host for preparation needs an active context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := refuseNestedHostPrepareScope(ctx); err != nil {
		return nil, err
	}
	if c.Now == nil {
		return nil, errNoEntry
	}
	if !ValidHostName(c.Now.Name) {
		return nil, badHostNameError(c.Now.Name)
	}
	if c.Was != nil && c.Was.Name != c.Now.Name {
		return nil, hostStateRemovalError("the host registration change has mismatched Was and Now names")
	}
	capture := &hostPrepareCommitCapture{name: c.Now.Name}
	bound := context.WithValue(ctx, hostPrepareCommitCaptureKey{}, capture)
	if err := UpdateHosts(bound, c); err != nil {
		return nil, err
	}
	if capture.target == nil || !capture.target.writerOrigin {
		return nil, hostStateRemovalError("the host commit did not produce a saved preparation target")
	}
	return capture.target, nil
}

func (c *hostPrepareCommitCapture) recordCommitted(ctx context.Context, change HostChange, out []Host) error {
	if err := retainInviteRegistrationCommitV1(ctx, change, out); err != nil {
		return err
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if c == nil || change.Now == nil || c.name != change.Now.Name {
		return hostStateRemovalError("the committed preparation target has no matching host change")
	}
	i := slices.IndexFunc(out, func(h Host) bool { return h.Name == c.name })
	if i < 0 || !SameEntry(out[i], *change.Now) {
		return hostStateRemovalError("the committed hosts list does not match the saved registration entry")
	}
	source, err := captureHostStateSource(ctx, out, false)
	if err != nil {
		return err
	}
	target, err := captureHostPrepareTargetLocked(ctx, out[i], source, true)
	if err != nil {
		return err
	}
	if change.HostID != "" && target.hostID != "" && target.hostID != change.HostID {
		return grantIdentityError(out[i])
	}
	c.target = target
	return nil
}

func captureHostPrepareTargetLocked(ctx context.Context, saved Host, source hostStateSource, fromRegistration bool) (*HostPrepareTargetV1, error) {
	if err := requireHostMembershipContext(ctx); err != nil {
		return nil, err
	}
	if !ValidHostName(saved.Name) || !source.exists || !ValidHostID(source.generation) {
		return nil, hostStateRemovalError("a preparation target requires a saved canonical host Source")
	}
	ts, err := readTiersFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, hostStateRemovalError("the host tier store is missing for this saved preparation target")
		}
		return nil, err
	}
	entry, hasEntry := ts.Entry(saved.Name)
	if entry.IDChanged {
		return nil, grantIdentityError(saved)
	}
	if entry.HostID != "" && !ValidHostID(entry.HostID) {
		return nil, grantIdentityError(saved)
	}
	expectedKey, err := verifyHostPreparePinKey(saved.Name, entry.Key, entry.Key != "")
	if err != nil {
		return nil, err
	}
	return &HostPrepareTargetV1{
		machineOwner:     sessionMachineOwnerOfV1(ctx),
		writerOrigin:     true,
		fromRegistration: fromRegistration,
		host:             cloneObservationHost(saved),
		source:           source,
		stateDir:         StateDir(),
		hostsPath:        hostsPath(),
		tier:             ts.Of(saved.Name),
		tierHasEntry:     hasEntry,
		tierNeedsAnswer:  ts.NeedsAnswer(saved.Name),
		tierHasKey:       entry.Key != "",
		hostID:           entry.HostID,
		key:              expectedKey,
	}, nil
}

// Host returns a defensive copy of the saved host entry bound to this target.
func (t *HostPrepareTargetV1) Host() Host {
	if t == nil || !t.writerOrigin {
		return Host{}
	}
	return cloneObservationHost(t.host)
}

// Name returns the saved host's name, or "" for an invalid target.
func (t *HostPrepareTargetV1) Name() string {
	if t == nil || !t.writerOrigin {
		return ""
	}
	return t.host.Name
}

// Tier returns the host tier captured when this target was opened or committed.
func (t *HostPrepareTargetV1) Tier() Tier {
	if t == nil || !t.writerOrigin {
		return TierUnknown
	}
	return t.tier
}

// HostID returns the saved host ID captured when this target was opened or
// committed, or "" if none was stored yet.
func (t *HostPrepareTargetV1) HostID() string {
	if t == nil || !t.writerOrigin {
		return ""
	}
	return t.hostID
}

// FromRegistration reports whether this target came directly from a committed
// UpdateHostsForPreparation registration save.
func (t *HostPrepareTargetV1) FromRegistration() bool {
	return t != nil && t.writerOrigin && t.fromRegistration
}

// Check verifies that the target's original membership Source, saved host
// entry, tier, host ID and pinned host key are all still current and unchanged.
func (t *HostPrepareTargetV1) Check(ctx context.Context) error {
	return t.checkCurrent(ctx, "")
}

// Dial opens a fresh saved-host bridge connection bound to this target's
// original membership Source. It refuses carried registration observations or
// held host scopes, checks the Source before transport startup, and closes the
// connection if the accepted hello does not match this saved Source.
func (t *HostPrepareTargetV1) Dial(ctx context.Context, interactive bool) (*Conn, error) {
	if err := t.checkDialContext(ctx); err != nil {
		return nil, err
	}
	if err := t.checkCurrent(ctx, ""); err != nil {
		return nil, err
	}
	c, err := dialWithPreparation(ctx, t.Host(), interactive, func(dctx context.Context, _ Transport) error {
		return withHostMembershipContext(dctx, func(locked context.Context) error {
			return t.checkLocked(locked, "")
		})
	})
	if err != nil {
		return nil, err
	}
	if err := t.checkConn(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// dialRoute exercises the real observed dial and post-hello saved Source check
// over an explicit transport route in package tests.
func (t *HostPrepareTargetV1) dialRoute(ctx context.Context, rt route) (*Conn, error) {
	if err := t.checkDialContext(ctx); err != nil {
		return nil, err
	}
	if err := t.checkCurrent(ctx, ""); err != nil {
		return nil, err
	}
	c, err := dial(ctx, t.Host(), rt)
	if err != nil {
		return nil, err
	}
	if err := t.checkConn(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (t *HostPrepareTargetV1) checkDialContext(ctx context.Context) error {
	if ctx == nil {
		return hostStateRemovalError("a preparation dial needs an active context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.checkValid(); err != nil {
		return err
	}
	if err := refuseNestedHostPrepareScope(ctx); err != nil {
		return err
	}
	if ctx.Value(heldKey{t.host.Name}) != nil || ctx.Value(hostStateObservationKey{}) != nil ||
		ctx.Value(hostTransportAttemptKey{}) != nil || ctx.Value(stageBindingKey{}) != nil {
		return hostStateRemovalError("preparation dial must start from a clean unheld caller context")
	}
	return nil
}

func (t *HostPrepareTargetV1) checkValid() error {
	if t == nil || !t.writerOrigin || !ValidHostName(t.host.Name) || !t.source.exists || !ValidHostID(t.source.generation) {
		return hostStateRemovalError("sandbox preparation needs a writer- or saved-source-derived host target")
	}
	if t.stateDir != StateDir() || t.hostsPath != hostsPath() {
		return hostStateRemovalError("the preparation target belongs to another local host domain")
	}
	return nil
}

func (t *HostPrepareTargetV1) checkCurrent(ctx context.Context, expectedHostID string) error {
	if ctx == nil {
		return hostStateRemovalError("revalidating a preparation target needs an active context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.checkValid(); err != nil {
		return err
	}
	return withHostMembershipContext(ctx, func(locked context.Context) error {
		return t.checkLocked(locked, expectedHostID)
	})
}

func (t *HostPrepareTargetV1) checkLocked(ctx context.Context, expectedHostID string) error {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if err := t.checkValid(); err != nil {
		return err
	}
	if err := t.source.check(ctx); err != nil {
		return err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == t.host.Name })
	if i < 0 || !SameEntry(hosts[i], t.host) {
		return hostStateRemovalError("the saved host changed after the preparation target was opened")
	}
	ts, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, hasEntry := ts.Entry(t.host.Name)
	if entry.IDChanged {
		return grantIdentityError(t.host)
	}
	if t.tierHasEntry && !hasEntry ||
		!t.tierHasEntry && hasEntry && (t.hostID != "" || expectedHostID == "" || !ValidHostID(expectedHostID) || entry.HostID != expectedHostID) ||
		ts.NeedsAnswer(t.host.Name) != t.tierNeedsAnswer || ts.Of(t.host.Name) != t.tier {
		return hostStateRemovalError("the host tier or trust state changed during preparation")
	}
	if t.hostID != "" {
		if entry.HostID != t.hostID || expectedHostID != "" && expectedHostID != t.hostID {
			return grantIdentityError(t.host)
		}
	} else if entry.HostID != "" {
		if expectedHostID == "" || !ValidHostID(expectedHostID) || entry.HostID != expectedHostID {
			return grantIdentityError(t.host)
		}
	}
	if err := t.checkPinLocked(entry); err != nil {
		return err
	}
	return ctx.Err()
}

func (t *HostPrepareTargetV1) checkPinLocked(entry TierEntry) error {
	if t.key == "" {
		if entry.Key != "" {
			return hostStateRemovalError("the host key record changed during preparation")
		}
		_, err := readPin(t.host.Name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return hostStateRemovalError("a pinned host key appeared after the preparation target was opened")
	}
	if t.tierHasKey {
		if entry.Key != t.key {
			return hostStateRemovalError("the host key record changed during preparation")
		}
	} else if entry.Key != "" && entry.Key != t.key {
		return hostStateRemovalError("the host key record changed during preparation")
	}
	_, err := verifyHostPreparePinKey(t.host.Name, t.key, true)
	return err
}

func (t *HostPrepareTargetV1) checkConn(ctx context.Context, c *Conn) error {
	if err := t.checkValid(); err != nil {
		return err
	}
	if c == nil || c.agentOnly || c.hostTransport == nil {
		return hostStateRemovalError("sandbox preparation needs an accepted saved-host connection")
	}
	if err := c.hostTransport.checkConn(c); err != nil {
		return err
	}
	if c.hostTransport.kind != hostTransportSaved {
		return hostStateRemovalError("a registration or provisional connection cannot promote to saved preparation")
	}
	if !ValidHostID(c.HostID()) || c.HostIDChanged() {
		return grantIdentityError(t.host)
	}
	if !SameEntry(t.host, c.Host) || !sameHostTransportSource(t.source, c.hostTransport.source) {
		return hostStateRemovalError("the connection belongs to another host or membership Source")
	}
	return t.checkCurrent(ctx, c.HostID())
}

func verifyHostPreparePinKey(name, wantKey string, requirePresent bool) (string, error) {
	keys, err := readPin(name)
	if errors.Is(err, os.ErrNotExist) {
		if requirePresent && wantKey != "" {
			return "", missingPinError(name, wantKey)
		}
		return "", nil
	}
	if err != nil {
		if isForeign(err) {
			return "", err
		}
		return "", &UserError{
			Msg:  fmt.Sprintf("%s's pinned host key cannot be read, so the client does not prepare it: %v", name, err),
			Hint: rekeyHint(name),
			Code: "host_key_unreadable",
			Kind: KindRefused,
		}
	}
	if len(keys) == 0 {
		return "", &UserError{
			Msg:  fmt.Sprintf("%s's pinned host key file holds no valid key", name),
			Hint: rekeyHint(name),
			Code: "host_key_unreadable",
			Kind: KindRefused,
		}
	}
	if wantKey != "" {
		if _, ok := withFingerprint(keys, wantKey); !ok {
			return "", mismatchedTierKeyError(name, wantKey, pinOf(keys, "").Fingerprint)
		}
		return wantKey, nil
	}
	return pinOf(keys, "").Fingerprint, nil
}

func refuseNestedHostPrepareScope(ctx context.Context) error {
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil ||
		ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil ||
		ctx.Value(hostTerminalInspectionKey{}) != nil {
		return hostStateRemovalError("host preparation cannot start inside a local state or membership callback")
	}
	return nil
}

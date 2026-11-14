package client

import (
	"context"
	"fmt"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Rekey gets h's host key again, as the first use of PinHostKey does: it
// connects once, offering no key and no password. With want, the key must
// have that fingerprint. A key that the user's SSH config revokes is
// refused. confirm gets the old fingerprint ("" when h has no pin, and the
// reason when its pin cannot be read) and the new one. When it returns
// nil, Rekey replaces the pin, and stops h's shared connection, which
// still checked the old key. When the host shows a key that the pin holds
// already, Rekey changes nothing, asks nothing, and returns the same
// fingerprint twice.
func Rekey(ctx context.Context, h Host, want string, confirm func(oldKey, newKey string) error) (oldKey, newKey string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if !ValidHostName(h.Name) {
		// No path of the pin's is built from it: it could leave the folder.
		return "", "", &UserError{Msg: fmt.Sprintf("%q cannot name a host", CleanLine(h.Name)),
			Hint: "Fix the host's name in " + hostsPath() + ", or " + brand.Name + " host rm it and add it again.", Code: "bad_name", Kind: KindUsage}
	}
	if err := checkFingerprint(want); err != nil {
		return "", "", err
	}
	ctx, unlockHost, err := LockHost(ctx, h.Name, "host rekey")
	if err != nil {
		return "", "", err
	}
	defer unlockHost()
	ctx, endMutation, err := holdSavedPinMutation(ctx, h, "host rekey")
	if err != nil {
		return "", "", err
	}
	defer endMutation()
	if err := checkRekeyHost(ctx, h); err != nil {
		return "", "", err
	}
	r, err := routeTo(ctx, h, false)
	if err != nil {
		return "", "", err
	}
	s, ok := r.t.(*SSH)
	if !ok || s.Host.SSH == "" {
		return "", "", &UserError{Msg: h.Name + " is not reached over SSH, so it has no host key to pin",
			Hint: brand.Name + " host ls", Code: "no_host_key", Kind: KindUsage}
	}
	if err := migratePinContext(ctx, h.Name); err != nil {
		return "", "", err
	}
	unlock, err := lockPin(ctx, h.Name)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	if err := stillSaved(h.Name); err != nil {
		return "", "", err
	}
	var old []hostKey
	if HasPin(h.Name) {
		if old, err = readPin(h.Name); isForeign(err) {
			return "", "", err // another host's pin: never replaced
		} else if err != nil {
			oldKey = "the pin cannot be read: " + CleanLine(err.Error())
		} else {
			oldKey = pinOf(old, "").Fingerprint
		}
	} else if wantKey, kerr := recordedTierKey(h.Name); kerr != nil {
		return "", "", kerr
	} else if wantKey != "" {
		oldKey = wantKey + " (the pin file is missing)"
	}
	keys, err := rekeyKeys(ctx, s.Host, want)
	if err != nil {
		return oldKey, "", err
	}
	newKey = pinOf(keys, "").Fingerprint
	for _, k := range keys {
		if holds(old, k) {
			return k.Fingerprint(), k.Fingerprint(), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return oldKey, newKey, err
	}
	if err := confirm(oldKey, newKey); err != nil {
		return oldKey, newKey, err
	}
	if err := ctx.Err(); err != nil {
		return oldKey, newKey, err
	}
	if err := ctx.Err(); err != nil {
		return oldKey, newKey, err
	}
	// Persist lower trust before the replacement identity can be used.
	if err := recordRekey(ctx, h, newKey); err != nil {
		return oldKey, newKey, err
	}
	if err := ctx.Err(); err != nil {
		return oldKey, newKey, err
	}
	if err := writePin(h.Name, nil, keys); err != nil {
		return oldKey, newKey, err
	}
	afterPinCommit()
	if err := retireMaster(context.WithoutCancel(ctx), s.Host); err != nil {
		return "", "", err
	}
	return oldKey, newKey, nil
}

// checkRekeyHost runs under HostLock, before taking the pin lock.
func checkRekeyHost(ctx context.Context, h Host) error {
	saved, err := SavedEntry(h.Name)
	if err != nil {
		return err
	}
	if !sameOrNone(saved, &h) {
		return (HostChange{Kind: "host rekey", Was: &h, Now: &h}).changed(saved)
	}
	return ensureTiersMigrated(ctx)
}

// rekeyKeys are the keys that host rekey pins: what the host shows now, of
// the fingerprint want when it is set, and not revoked by the user's SSH
// config.
func rekeyKeys(ctx context.Context, h Host, want string) ([]hostKey, error) {
	t, err := trustHostKey(ctx, h, "")
	if err != nil {
		return nil, err
	}
	if t.kind == trustRevoked {
		return nil, revokedError(h, t.fp)
	}
	var keys []hostKey
	if want != "" {
		var k hostKey
		k, err = fetchWanted(ctx, h, want)
		keys = []hostKey{k}
	} else {
		keys, err = fetchHostKey(ctx, h, false, algorithmsFor(t.typ))
	}
	if err == nil {
		err = notRevoked(ctx, h, t, keys)
	}
	if err != nil {
		return nil, err
	}
	return keys, nil
}

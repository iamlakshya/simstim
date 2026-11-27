package client

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/join/code"
)

func checkFingerprintSet(want string, wants []string) error {
	if err := checkFingerprint(want); err != nil {
		return err
	}
	if len(wants) > 8 {
		return inviteError("bad_value", "the reply names too many SSH host keys", "Use the reply printed by the machine.")
	}
	if want != "" && (len(wants) > 0 && (len(wants) != 1 || wants[0] != want)) {
		return inviteError("bad_value", "the single host key and host-key list disagree", "Pass either the single key, or the complete list from the reply.")
	}
	for n, fp := range wants {
		if !code.ValidFingerprint(fp) || slices.Contains(wants[:n], fp) {
			return inviteError("bad_value", "the host-key list contains an invalid or repeated fingerprint", "Pass each SHA256 fingerprint from the machine exactly once.")
		}
	}
	return nil
}
func keptPinSet(ctx context.Context, name, want string, wants []string) (Pin, error) {
	p, err := keptPin(ctx, name, want)
	if err != nil {
		return p, err
	}
	for _, fp := range wants {
		if _, err = keptPin(ctx, name, fp); err != nil {
			return Pin{}, err
		}
	}
	return p, nil
}
func findHostKeySet(ctx context.Context, h Host, want string, wants []string, firstUse FirstUse) ([]hostKey, PinSource, error) {
	if len(wants) == 0 {
		return findHostKey(ctx, h, want, firstUse)
	}
	var keys []hostKey
	for _, fp := range wants {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		found, _, err := findHostKey(ctx, h, fp, FirstUseRefuse)
		if err != nil {
			return nil, "", err
		}
		if len(found) != 1 || found[0].Fingerprint() != fp {
			return nil, "", inviteError("host_key_changed", "the machine did not show every host key from its reply", "Compare its current host keys on the machine, then make a fresh reply.")
		}
		if !holds(keys, found[0]) {
			keys = append(keys, found[0])
		}
	}
	return keys, PinMatchedFlag, nil
}

// WithAcceptedInviteKeys carries explicitly compared keys into the existing
// registration pin stage. It grants no connection, registration or save.
func WithAcceptedInviteKeys(ctx context.Context, fingerprints []string) context.Context {
	return context.WithValue(ctx, acceptedInviteKeys{}, slices.Clone(fingerprints))
}

type acceptedInviteKeys struct{}

// AcceptedInviteKeys returns a copy of the compared list for the CLI's ordinary
// host-add pin options. Empty means the caller did not accept an invite.
func AcceptedInviteKeys(ctx context.Context) []string {
	v, _ := ctx.Value(acceptedInviteKeys{}).([]string)
	return slices.Clone(v)
}

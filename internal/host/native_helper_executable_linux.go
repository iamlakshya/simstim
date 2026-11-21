//go:build linux

package host

import (
	"errors"
	"os"
	"slices"
	"strings"
)

// checkCreatedHelperArgumentsV1 is only the public pre-loan creation gate.
// It supplies no executable identity or private handoff permission by itself.
func checkCreatedHelperArgumentsV1(peer *nativeStartupPeerV1, argv []string) error {
	if err := peer.checkLive(); err != nil {
		return err
	}
	raw, err := peer.proc.ReadFile("cmdline")
	if err != nil {
		return err
	}
	actual := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if !slices.Equal(actual, argv) {
		return sessionLeaseError("the actual created helper did not receive its closed literal command")
	}
	return peer.checkLive()
}
func checkNativeHelperExecutableV1(peer *nativeStartupPeerV1, expected os.FileInfo, argv []string) error {
	if peer == nil || peer.identity == nil {
		return sessionLeaseError("the original helper has not handed off its actual native code/namespace identity")
	}
	return errors.Join(peer.identity.Check(expected), checkCreatedHelperArgumentsV1(peer, argv))
}

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// SetDefaultHost changes only the canonical document's local default. An empty
// name clears it. It never changes host rows, credentials or machine state.
func SetDefaultHost(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostStateObservationKey{}) != nil {
		return hostStateRemovalError("a default-host edit cannot borrow another host operation or membership callback")
	}
	var expected *Host
	if name != "" {
		if !ValidHostName(name) {
			return badHostNameError(name)
		}
		host, err := FindHost(name)
		if err != nil {
			return err
		}
		owned, release, err := LockHost(ctx, name, "host default")
		if err != nil {
			return err
		}
		defer release()
		ctx = owned
		if _, err := ResolveHostTier(ctx, host); err != nil {
			return err
		}
		expected = &host
	}
	locked, release, err := acquireHostMembershipWriteContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	file, err := loadHostsFile()
	if err != nil {
		return err
	}
	if expected != nil {
		current, err := findPlacementHost(file.Hosts, name)
		if err != nil {
			return err
		}
		if !SameEntry(current, *expected) {
			return grantIdentityError(*expected)
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		if err := Allow(current, tiers.Of(name), Outbound{Kind: OutboundDefaultHost}, DefaultSettings()); err != nil {
			return err
		}
	}
	if file.Default == name {
		return locked.Err()
	}
	raw, err := ReadConfigFile(hostsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	document := map[string]any{}
	if len(raw) != 0 {
		if _, err := toml.Decode(string(raw), &document); err != nil {
			return tomlError(hostsPath(), string(raw), err)
		}
	}
	if name == "" {
		delete(document, "default")
	} else {
		document["default"] = name
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(document); err != nil {
		return fmt.Errorf("encode the default host: %w", err)
	}
	var next hostsFile
	if _, err := decodeConfig(encoded.String(), &next); err != nil {
		return tomlError(hostsPath(), encoded.String(), err)
	}
	if next.Default != name || !sameHostDocumentRows(next.Hosts, file.Hosts) {
		return hostStateRemovalError("a default-host edit changed saved-host rows")
	}
	generation, err := newMembershipGeneration()
	if err != nil {
		return fmt.Errorf("make the default-host membership generation: %w", err)
	}
	beforeHostsSave()
	if err := locked.Err(); err != nil {
		return err
	}
	return writeHostsDocumentGeneration(encoded.Bytes(), generation)
}

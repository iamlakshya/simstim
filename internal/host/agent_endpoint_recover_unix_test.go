//go:build !windows

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// Release exactly the handles the kernel releases on exit, without calling
// any namespace cleanup. Recovery therefore sees actual retained inodes.
func abandonAgentEndpoint(t *testing.T, endpoint *hostAgentEndpoint) {
	t.Helper()
	endpoint.revoke()
	if err := endpoint.claim.Close(); err != nil {
		t.Fatal(err)
	}
	if endpoint.lease != nil {
		if err := endpoint.lease.root.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = endpoint.root.Close() })
}

func requireAgentEntries(t *testing.T, endpoint *hostAgentEndpoint, absent bool, names ...string) {
	t.Helper()
	for _, name := range names {
		_, err := endpoint.root.Lstat(name)
		if absent && !errors.Is(err, os.ErrNotExist) || !absent && err != nil {
			t.Fatalf("entry %q absent=%v: %v", name, absent, err)
		}
	}
}

func TestAgentEndpointRecoverySkipsLiveClaimsAndRecoversDeadOwnedChain(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	if err := endpoint.publish(t.Context(), "abc23def"); err != nil {
		t.Fatal(err)
	}
	lease := endpoint.lease
	names := []string{endpoint.name, endpoint.witness, endpoint.claimName, lease.name, lease.alias, lease.aliasWitness, lease.linkWitness}
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	requireAgentEntries(t, endpoint, false, names...)
	abandonAgentEndpoint(t, endpoint)
	// The real next-prepare path performs recovery before claiming its own
	// lease; the old publication must not survive behind the fresh endpoint.
	next, err := prepareHostAgentEndpointContext(t.Context(), agentbridge.Scope{Session: "abc23def", Lease: strings.Repeat("b", 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	requireAgentEntries(t, endpoint, true, names...)
}

func TestAgentEndpointRecoveryPreservesUnlockedLegacyClaims(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	if err := endpoint.claim.Truncate(0); err != nil {
		t.Fatal(err)
	}
	abandonAgentEndpoint(t, endpoint)
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	requireAgentEntries(t, endpoint, false, endpoint.name, endpoint.witness, endpoint.claimName)
}

func TestAgentEndpointRecoveryPreservesReplacedSocketsAndAllProof(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	abandonAgentEndpoint(t, endpoint)
	if err := endpoint.root.Remove(endpoint.name); err != nil {
		t.Fatal(err)
	}
	const foreign = "generated replacement fixture"
	if err := endpoint.root.WriteFile(endpoint.name, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	value, err := endpoint.root.ReadFile(endpoint.name)
	if err != nil || string(value) != foreign {
		t.Fatal("recovery changed the replacement socket entry")
	}
	requireAgentEntries(t, endpoint, false, endpoint.witness, endpoint.claimName)
}

func TestAgentEndpointRecoveryPreservesSameTargetReplacementAliases(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	if err := endpoint.publish(t.Context(), "abc23def"); err != nil {
		t.Fatal(err)
	}
	lease := endpoint.lease
	abandonAgentEndpoint(t, endpoint)
	if err := endpoint.root.Remove(lease.alias); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.root.Symlink(endpoint.path, lease.alias); err != nil {
		t.Fatal(err)
	}
	replacement, err := endpoint.root.Lstat(lease.alias)
	if err != nil || os.SameFile(replacement, lease.aliasInfo) {
		t.Fatal("the retained witness did not distinguish the replacement inode")
	}
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	current, err := endpoint.root.Lstat(lease.alias)
	if err != nil || !os.SameFile(current, replacement) {
		t.Fatal("recovery removed a same-target replacement alias")
	}
	requireAgentEntries(t, endpoint, false, endpoint.name, endpoint.witness, endpoint.claimName, lease.aliasWitness, lease.linkWitness)
}

func TestAgentEndpointRecoveryKeepsUnwitnessedCrashGaps(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	abandonAgentEndpoint(t, endpoint)
	if err := endpoint.root.Remove(endpoint.witness); err != nil {
		t.Fatal(err)
	}
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	requireAgentEntries(t, endpoint, false, endpoint.name, endpoint.claimName)
}

func TestAgentEndpointRecoveryCancellationMakesNoNamespaceChange(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	abandonAgentEndpoint(t, endpoint)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := recoverHostAgentEndpoints(ctx, endpoint.root); !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery lost its cancellation: %v", err)
	}
	requireAgentEntries(t, endpoint, false, endpoint.name, endpoint.witness, endpoint.claimName)
}

func TestAgentEndpointClaimDoesNotAuthoriseAnUnrelatedAlias(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	alias := ".agent-abc23def-" + strings.Repeat("f", 32) + ".sock"
	if err := endpoint.root.Symlink(endpoint.path, alias); err != nil {
		t.Fatal(err)
	}
	abandonAgentEndpoint(t, endpoint)
	if err := recoverHostAgentEndpoints(t.Context(), endpoint.root); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(endpoint.root.Name(), alias))
	if err != nil || target != endpoint.path {
		t.Fatal("claim recovery adopted an unrelated alias with the same target")
	}
}

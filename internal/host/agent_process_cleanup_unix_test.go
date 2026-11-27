//go:build !windows

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func processCleanupEndpoint(t *testing.T) *hostAgentEndpoint {
	t.Helper()
	agentBridgeEnv(t)
	endpoint, err := prepareHostAgentEndpoint(agentbridge.Scope{Session: "abc23def", Lease: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestAgentEndpointClaimLockCoversItsLiveLifetime(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	defer endpoint.Close()
	observer, err := endpoint.root.OpenFile(endpoint.claimName, os.O_RDWR|agentNoFollow|agentNonblock, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	locked, err := tryAgentLock(observer)
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		_ = releaseAgentLock(observer)
		t.Fatal("a second owner acquired the live endpoint claim")
	}
}

func TestAgentEndpointKeepsProofWhenTheSocketNameIsReplaced(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	root := endpoint.root.Name()
	socket := filepath.Join(root, endpoint.name)
	witness := filepath.Join(root, endpoint.witness)
	claim := filepath.Join(root, endpoint.claimName)
	if err := endpoint.root.Remove(endpoint.name); err != nil {
		t.Fatal(err)
	}
	const foreign = "generated foreign replacement fixture"
	if err := os.WriteFile(socket, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.CloseContext(t.Context()); err == nil {
		t.Fatal("a foreign replacement was treated as completed owned cleanup")
	}
	if body, err := os.ReadFile(socket); err != nil || string(body) != foreign {
		t.Fatal("cleanup removed a foreign replacement")
	}
	for _, path := range []string{witness, claim} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("failed cleanup destroyed its durable proof: %v", err)
		}
	}
}

func TestAgentEndpointSharesCancellationWithLeaseCleanup(t *testing.T) {
	endpoint := processCleanupEndpoint(t)
	if err := endpoint.publish(t.Context(), "abc23def"); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockAgentLink(t.Context(), endpoint.root, "abc23def")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = context.WithValue(ctx, agentLinkContentionKey{}, func() { cancel() })
	finished := make(chan error, 1)
	go func() { finished <- endpoint.CloseContext(ctx) }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lease cleanup dropped its shared cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup started an independent wait after caller cancellation")
	}
	for _, name := range []string{endpoint.name, endpoint.witness, endpoint.claimName} {
		if _, err := os.Lstat(filepath.Join(endpoint.root.Name(), name)); err != nil {
			t.Fatal("failed lease cleanup lost socket ownership proof")
		}
	}
}

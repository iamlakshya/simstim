//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture exercises real tar/ext4 bytes locally. It supplies no released
// asset pin, installed host authority, jail, namespace or native VM proof.
func agentsStoreBuilder(t *testing.T, text string) (AgentsBuilderV1, *atomic.Int32) {
	t.Helper()
	folder, root := agentTestFolder(t)
	name := filepath.Join(folder, "agent")
	if err := os.WriteFile(name, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	inputs := []AgentsInputV1{{HostPath: "/test/install", Folder: root}}
	binaries := []AgentBinaryV1{{Name: "agent", ResolvedPath: "/test/install/agent"}}
	conversions := new(atomic.Int32)
	return func(ctx context.Context, previous string, output *os.File) (AgentsBuildResultV1, error) {
		source, err := AgentsSourceDigestV1(ctx, inputs, binaries)
		result := AgentsBuildResultV1{SourceDigest: source, Agents: []string{"fixture"}, Reused: previous != "" && previous == source}
		if err != nil || result.Reused {
			return result, err
		}
		var raw bytes.Buffer
		if _, err := WriteAgentsTarV1(ctx, inputs, binaries, &raw); err != nil {
			return result, err
		}
		conversions.Add(1)
		return result, ConvertAgentsV1(ctx, &raw, output)
	}, conversions
}

func TestAgentsEnsureReusesUnchangedDiskWhileRunningLeaseRemainsHeld(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, conversions := agentsStoreBuilder(t, "installed executable")
	first, running, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	second, selected, err := store.EnsureAgentsV1(ctx, runtime.GOARCH, build)
	if err != nil {
		t.Fatal("unchanged start waited for running VM to release its disk", err)
	}
	defer selected.Close()
	if first.Content != second.Content || first.SourceDigest != second.SourceDigest || conversions.Load() != 1 {
		t.Fatal("unchanged installs were reconverted or changed pins", first, second, conversions.Load())
	}
	if _, acquired, err := store.tryExistingLease(ctx, "agents/"+first.Content.SHA256+".lock", false); err != nil || acquired {
		t.Fatal("returned retention leases did not protect exact selected bytes", acquired, err)
	}
}

func TestAgentsEnsurePreservesSameDigestForeignReplacement(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "installed executable")
	disk, lease, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(store.root.Name(), "agents", disk.Content.SHA256+".img")
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	twin := filepath.Join(filepath.Dir(name), "foreign-twin")
	if err := os.WriteFile(twin, before, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(twin, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(twin, name); err != nil {
		t.Fatal(err)
	}
	if file, held, err := store.OpenAgentsV1(t.Context(), disk.Content); err == nil {
		file.Close()
		held.Close()
		t.Fatal("byte equality adopted another native inode")
	}
	if after, err := os.ReadFile(name); err != nil || !bytes.Equal(after, before) {
		t.Fatal("foreign bytes were changed", err)
	}
}

func TestAgentsEnsureCancellationCleansOnlyItsFreshPart(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	ctx, cancel := context.WithCancel(t.Context())
	_, _, err := store.EnsureAgentsV1(ctx, runtime.GOARCH, func(context.Context, string, *os.File) (AgentsBuildResultV1, error) {
		cancel()
		return AgentsBuildResultV1{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was hidden", err)
	}
	root, err := store.agentsRoot(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil || len(names) != 0 {
		t.Fatal("cancelled creator left a part or published metadata", names, err)
	}
}

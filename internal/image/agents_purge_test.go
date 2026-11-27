//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func noAgentsReferences() (map[string]string, error) { return map[string]string{}, nil }

func TestAgentsTotalPurgeUsesExistingDomainAndIgnoresNewestRetention(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "installed executable")
	_, lease, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	existing, err := OpenHostExisting(t.Context(), store.root.Name(), store.domain)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	report, err := existing.PurgeAgentsOwned(t.Context(), noAgentsReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatal("owned total purge failed", report, err)
	}
	if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents")); !os.IsNotExist(err) {
		t.Fatal("owned namespace survived total purge", err)
	}
	if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents.lock")); !os.IsNotExist(err) {
		t.Fatal("owned source lease survived total purge", err)
	}
}

func TestAgentsTotalPurgePreservesUnknownFilesAndNativeNamespace(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "installed executable")
	_, lease, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	name := filepath.Join(store.root.Name(), "agents", "foreign-file")
	data := []byte("unowned bytes must remain")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.PurgeAgentsOwned(t.Context(), noAgentsReferences)
	if err != nil || len(report.Left) == 0 {
		t.Fatal("unknown entry was not reported", report, err)
	}
	after, err := os.Lstat(name)
	actual, readErr := os.ReadFile(name)
	if err != nil || readErr != nil || !os.SameFile(before, after) || !bytes.Equal(data, actual) {
		t.Fatal("foreign entry changed", err, readErr)
	}
}

func TestAgentsSourcePurgeRetriesCommittedIntentAfterCloseFailure(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "installed executable")
	_, lease, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	previous := closeImageMetadata
	t.Cleanup(func() { closeImageMetadata = previous })
	failure := errors.New("source intent close failed after its actual commit")
	injected := false
	closeImageMetadata = func(file *os.File) error {
		var raw [4096]byte
		n, _ := file.ReadAt(raw[:], 0)
		selected := strings.HasPrefix(filepath.Base(file.Name()), ".receipt-new-") && bytes.Contains(raw[:n], []byte(`"leaf":".agents-source-purge-`)) && !injected
		closeErr := previous(file)
		if selected {
			injected = true
			return errors.Join(closeErr, failure)
		}
		return closeErr
	}
	if _, err := store.PurgeAgentsOwned(t.Context(), noAgentsReferences); !errors.Is(err, failure) || !injected {
		t.Fatal("close fault became purge success", err, injected)
	}
	closeImageMetadata = previous
	report, err := store.PurgeAgentsOwned(t.Context(), noAgentsReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatal("native source-intent replay failed", report, err)
	}
	if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents")); !os.IsNotExist(err) {
		t.Fatal("replayed directory remains", err)
	}
}

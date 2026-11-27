package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostLedgerUnobservedBackgroundWritesRefuseBeforeNamespace(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "ledger.example"}
	for _, write := range []func() error{
		func() error {
			return RecordGrant(h, strings.Repeat("a", 32), Outbound{Kind: OutboundSecret, Name: "TEST"})
		},
		func() error {
			return RecordGrantContext(context.Background(), h, strings.Repeat("a", 32), Outbound{Kind: OutboundSecret, Name: "TEST"})
		},
		func() error { return MarkGrantsRemoved(h) },
		func() error { return MarkGrantsRemovedContext(context.Background(), h) },
		func() error {
			return withStateFile(context.Background(), nil, grantsFileName, true, func([]byte, bool) ([]byte, error) { t.Fatal("unobserved raw ledger callback ran"); return nil, nil })
		},
	} {
		if err := write(); err == nil {
			t.Fatal("Background ledger write acquired state authority")
		}
	}
	terminalAssertAbsent(t, StateDir(), ConfigDir())
	if rows, err := LoadGrantsContext(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("unobserved absent readonly ledger semantics changed: %+v, %v", rows, err)
	}
	terminalAssertAbsent(t, StateDir())
}

func TestHostLedgerUnobservedWritePreservesExistingBytesAndReadonly(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "ledger.example"}
	when := nowUTC()
	raw, err := encodeGrantsFile(grantsFile{Version: 1, Grants: []GrantRecord{{HostName: h.Name, HostID: strings.Repeat("a", 32), Kind: OutboundSecret, Name: "TEST", FirstAt: when, LastAt: when, Count: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(StateDir(), grantsFileName)
	// Deliberate local fixture bytes, not an authorised production writer.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() error{
		func() error {
			return RecordGrantContext(context.Background(), h, strings.Repeat("a", 32), Outbound{Kind: OutboundSecret, Name: "TEST"})
		},
		func() error { return MarkGrantsRemovedContext(context.Background(), h) },
	} {
		if err := write(); err == nil {
			t.Fatal("unobserved existing ledger mutation succeeded")
		}
	}
	after, err := os.Stat(path)
	got, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !os.SameFile(before, after) || !bytes.Equal(got, raw) {
		t.Fatalf("refused ledger mutation changed native inode or bytes: %v, %v", err, readErr)
	}
	terminalAssertAbsent(t, path+".lock")
	rows, err := LoadGrantsContext(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Count != 1 || rows[0].RemovedAt != nil {
		t.Fatalf("unobserved readonly current ledger semantics changed: %+v, %v", rows, err)
	}
}

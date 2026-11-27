//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRewindTokenAndJournalCannotCreateNativeOrigin(t *testing.T) {
	operation := &SessionOperation{id: "parent23"}
	p := &bridgeRewindPreparationV1{operation: operation, token: string(make([]byte, 64)), prepareRequest: 1, commitRequest: 2, consumed: true, commitEncoded: []byte(`{}`), execution: t.Context()}
	p.active.Store(true)
	ctx := context.WithValue(t.Context(), bridgeRewindPreparationContextKeyV1{}, p)
	if _, err := requireResumePreparationAccountingOriginV1(ctx, operation); err == nil {
		t.Fatal("a context and token created an actual cold accounting origin")
	}
	if err := p.checkCommittedV1(ctx, operation, &ColdResumeV1{}, api.ResumeArgs{ID: operation.id}); err == nil {
		t.Fatal("data created a closed native rewind commit")
	}
	for _, phase := range []string{"prepared", "disk_exchanged", "metadata_committed", "ready_to_restart", "completed"} {
		p.journal = &rewindJournalOwnerV1{preparation: p, data: rewindJournalDataV1{Version: 1, Primary: operation.id, Phase: phase}}
		if err := p.journal.checkV1(ctx); err == nil {
			t.Fatalf("journal phase %s replaced its original OP and creator", phase)
		}
	}
}

func TestPendingRewindRefusesUnrelatedActualOperation(t *testing.T) {
	isolatedStoreHostConstruction(t)
	m := &Manifest{Schema: 1, ID: "parent23", Name: "original", CreatedAt: time.Now().UTC()}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	operation, err := BeginSessionOperation(t.Context(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	if err := checkRewindJournalForStartV1(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte(`{}`), []byte(`{"v":1,"phase":"ready_to_restart"}`), []byte(`{"v":1,"phase":"completed"}`)} {
		if err := os.WriteFile(paths.SessionDir(m.ID)+"/"+rewindJournalLeafV1, data, 0600); err != nil {
			t.Fatal(err)
		}
		var wire *proto.Error
		if err := checkRewindJournalForStartV1(t.Context(), operation); !errors.As(err, &wire) || wire.Code != "rewind_pending" {
			t.Fatalf("journal DATA authorised startup: %v", err)
		}
	}
}

func TestRewindMetadataRequiresActualWholeStoppedCustody(t *testing.T) {
	for _, stopped := range []*StoppedSessionV1{nil, {}, {settled: true}, {settled: true, holderHanded: true}} {
		p := &bridgeRewindPreparationV1{operation: &SessionOperation{id: "parent23"}, stopped: stopped}
		metadata := checkpoint.Metadata{AgentSessionIDs: map[string]string{"parent23": ""}}
		if owner, err := prepareRewindMetadataV1(t.Context(), p, "0123456789abcdef0123456789abcdef", metadata); err == nil || owner != nil {
			t.Fatal("a stopped row changed trusted conversation metadata")
		}
	}
}

func TestOriginalForkDataNeedsRealOriginalBootBeforeOpeningAnyFile(t *testing.T) {
	ctx := context.WithValue(t.Context(), originalBootContextKeyV1{}, &OriginalVMNeed{})
	called := false
	if provided, err := withOriginalForkDiskDataV1(ctx, func(*os.File) error { called = true; return nil }); err == nil || provided || called {
		t.Fatal("a context DTO supplied fork disk data")
	}
}

func TestClosedRewindWireRejectsResumePreparedToken(t *testing.T) {
	args := api.RewindCommitArgsV1{V: 1, Prepared: &api.PreparedRewindV1{V: 1, Token: strings.Repeat("a", 64)}, Selection: api.RewindArgs{ID: "parent23", Checkpoint: "20261121T010203.000Z"}, Start: api.ResumeArgs{ID: "parent23", Snapshot: &api.SandboxSnapshot{Version: 1, Primary: "parent23", Generation: strings.Repeat("b", 64), Members: []string{}}}}
	if err := args.Validate(); err != nil {
		t.Fatal(err)
	}
	args.Start.Prepared = &api.PreparedResumeV1{V: 1, Token: strings.Repeat("c", 64)}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var received api.RewindCommitArgsV1
	if err := json.Unmarshal(raw, &received); err == nil && received.Validate() == nil {
		t.Fatal("a Resume prepared DTO entered the closed rewind path")
	}
}

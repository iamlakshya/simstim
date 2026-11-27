package inputtransfer

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func footprintSummary() api.InputSummary {
	return api.InputSummary{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64),
		FileCount: 10000, CopyBytes: api.DefaultCopyMax, Identity: api.GitIdentity{Known: true},
		Repositories: []api.InputRepository{
			{Name: "one", Commit: strings.Repeat("c", 40), ObjectFormat: "sha1", Size: api.DefaultSeedMax, SHA256: strings.Repeat("d", 64)},
			{Name: "two", Commit: strings.Repeat("e", 40), ObjectFormat: "sha1", Size: api.DefaultSeedMax - 1, SHA256: strings.Repeat("f", 64)},
		}}
}

func TestStagingReservesAllDiskButOnlyLargestOneSealedBundle(t *testing.T) {
	summary := footprintSummary()
	plan, err := PlanStaging(summary, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DiskBytes < 2*api.DefaultSeedMax+api.DefaultCopyMax || plan.SealedBytes != api.DefaultSeedMax ||
		plan.MetadataBytes < int64(summary.FileCount)*4095*6 || plan.StagedFiles != 10003 {
		t.Fatalf("reservation = %+v", plan)
	}
	if plan.MetadataBytes >= int64(summary.FileCount)*FrameMax {
		t.Fatal("storage reservation reused padded wire-work allocation")
	}
}

func TestStagingGeometryAndOverflowRefuseBeforeUse(t *testing.T) {
	summary := footprintSummary()
	if _, err := PlanStaging(summary, 0, 4096); err == nil {
		t.Fatal("missing allocation geometry accepted")
	}
	summary.Repositories[0].Size = 1<<63 - 1
	if _, err := PlanStaging(summary, 4096, 4096); err == nil {
		t.Fatal("rounding overflow became a small reservation")
	}
}

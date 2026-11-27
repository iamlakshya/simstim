package inputtransfer

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestWireAllocationAccountsPaddingAndOneByteChunksWithoutAggregateCap(t *testing.T) {
	summary := api.InputSummary{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64),
		FileCount: 10000, CopyBytes: api.DefaultCopyMax, Identity: api.GitIdentity{Known: true}}
	for _, name := range []string{"first", "second"} {
		summary.Repositories = append(summary.Repositories, api.InputRepository{Name: name, SourceBranch: "refs/heads/main",
			Commit: strings.Repeat("c", 40), ObjectFormat: "sha1", Size: api.DefaultSeedMax, SHA256: strings.Repeat("d", 64)})
	}
	work, err := WireWork(summary)
	if err != nil {
		t.Fatal(err)
	}
	if work < 6*(2*api.DefaultSeedMax+api.DefaultCopyMax) || work < int64(summary.FileCount)*int64(FrameMax+4)*3 {
		t.Fatal("allocation assumed large canonical chunks or omitted accepted control padding")
	}
}

func TestWireAllocationOverflowRefusesBeforeUse(t *testing.T) {
	summary := api.InputSummary{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64),
		Identity: api.GitIdentity{Known: true}, Repositories: []api.InputRepository{{Name: "source", Commit: strings.Repeat("c", 40),
			ObjectFormat: "sha1", Size: 1<<63 - 1, SHA256: strings.Repeat("d", 64)}}}
	if _, err := WireWork(summary); err == nil {
		t.Fatal("overflow became a smaller fixed deadline")
	}
}

package host

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestSavedImageMetadataCannotIssueOriginalStoreAdmission(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	selected := image.Request{Ref: "fixture_saved", Digest: digest, PlatformDigest: digest, Arch: "amd64", RootHash: strings.Repeat("b", 64), DataBlocks: 4, SavedHost: &image.SavedHostV1{Version: 1, HostID: strings.Repeat("c", 32), Ref: "fixture_saved", Digest: digest}}
	spec := wall.Spec{VM: &wall.VMSpec{Version: 1, Image: selected}}
	if err := selected.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.Background(), wall.WithExpectedSpec(context.Background(), spec), context.WithValue(context.Background(), originalBootContextKeyV1{}, &OriginalVMNeed{})} {
		if err := checkOriginalSavedImageNeedV1(ctx, spec); err == nil {
			t.Fatal("saved metadata or projected spec issued native store admission")
		}
	}
	ordinary := spec
	copy := *ordinary.VM
	ordinary.VM = &copy
	ordinary.VM.Image.SavedHost = nil
	if err := checkOriginalSavedImageNeedV1(context.Background(), ordinary); err != nil {
		t.Fatal("ordinary OCI admission acquired a saved-root dependency", err)
	}
}

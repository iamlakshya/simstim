package guest

import (
	"github.com/iamlakshya/simstim/internal/proto"
	"testing"
)

func TestMemberCannotChangeOrInventInstalledDecoyGeneration(t *testing.T) {
	g := &Init{decoysApplied: true}
	if err := g.checkBootDecoys(nil); err != nil {
		t.Fatal("disabled generation changed", err)
	}
	if err := g.checkBootDecoys(&proto.DecoyFilesV1{Version: 1}); err == nil {
		t.Fatal("member invented a generation")
	}
}

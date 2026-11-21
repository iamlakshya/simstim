//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeCopyHelloCannotAdoptNilOrCopiedCreatorData(t *testing.T) {
	line := &StartupLineV1{}
	for _, owner := range []*purgeAttemptExecV1{nil, {}, {line: line, start: &purgeAttemptStartV2{}}} {
		if err := line.acceptOriginalPurgeAttemptHelloV2(context.Background(), proto.Frame{Type: proto.THello}, owner); err == nil {
			t.Fatal("data without its actual original Start accepted a copy hello")
		}
	}
	if line.nativePeer != nil {
		t.Fatal("a refused copy frame issued a native peer")
	}
}

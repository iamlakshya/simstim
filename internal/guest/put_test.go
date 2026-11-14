package guest

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPutTargetStaysInFixedPasteFolder(t *testing.T) {
	header := proto.FilePutHeaderV1{Version: 1, Name: "shot.png", Size: 5, SHA256: strings.Repeat("a", 64)}
	top, relative, err := putTarget(header)
	if err != nil || top != host.GuestRunDir()+"/paste" || relative != "aaaaaaaaaaaa-shot.png" {
		t.Fatalf("paste target = %q/%q, %v", top, relative, err)
	}
	for _, modify := range []func(*proto.FilePutHeaderV1){
		func(h *proto.FilePutHeaderV1) { h.Version++ },
		func(h *proto.FilePutHeaderV1) { h.Name = "../outside" },
		func(h *proto.FilePutHeaderV1) { h.SHA256 = strings.Repeat("x", 64) },
		func(h *proto.FilePutHeaderV1) { h.Replace = true },
		func(h *proto.FilePutHeaderV1) { h.Size = host.MaxUpload + 1 },
		func(h *proto.FilePutHeaderV1) { h.Path = "/etc/passwd" },
		func(h *proto.FilePutHeaderV1) { h.Path = "/work/app/../outside" },
		func(h *proto.FilePutHeaderV1) { h.Path = "/work/app/.git/config" },
	} {
		changed := header
		modify(&changed)
		if _, _, err := putTarget(changed); err == nil {
			t.Fatalf("unsafe put declaration passed: %+v", changed)
		}
	}
}

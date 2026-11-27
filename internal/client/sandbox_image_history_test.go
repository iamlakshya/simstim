package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestOriginalVMImageHistoryRequiresSamePrivatePlanSelection(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
	request := image.Request{Ref: "ghcr.io/example/tools:stable", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 1}
	selected := &sandboxImageSelectionV1{Version: 1, Origin: imageOriginOCI, Image: request.Clone()}
	plan := &VMNeedPlanV1{conn: c, hostID: c.HostID(), need: vmneed.NeedV1{BootV1: vmneed.BootV1{Image: request.Clone()}}, imageSelection: selected}
	raw, err := originalVMImageHistoryV1(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSandboxImageSelection(raw)
	if err != nil || !sameSandboxImageSelection(selected, decoded) {
		t.Fatal("original selection was replaced", err)
	}
	plan.imageSelection.Image.PlatformDigest = "sha256:" + strings.Repeat("d", 64)
	if _, err := originalVMImageHistoryV1(plan); err == nil {
		t.Fatal("mismatching private selection minted creation history")
	}
	plan.imageSelection = nil
	if _, err := originalVMImageHistoryV1(plan); err == nil {
		t.Fatal("raw Need minted a history discriminator")
	}
}

func TestSandboxImageHistoryKeepsOpaqueExactBytesAndRejectsRetryReplacement(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	p.imageSelectionRaw = json.RawMessage(`{ "version":2,"future":{"duplicate":1,"duplicate":2} }`)
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || loaded == nil || !bytes.Equal(loaded.imageSelectionRaw, p.imageSelectionRaw) {
		t.Fatal("opaque historical bytes changed", err)
	}
	cloned := cloneSandboxPolicy(loaded)
	cloned.imageSelectionRaw[0] = '['
	if loaded.imageSelectionRaw[0] != '{' {
		t.Fatal("clone aliases original history")
	}
	if _, err := decodeSandboxImageSelection(loaded.imageSelectionRaw); err == nil {
		t.Fatal("future history granted understood image selection")
	}
	p.imageSelectionRaw = json.RawMessage(`{"version":2,"different":true}`)
	if err := reserveSandbox(t.Context(), h, p); err == nil {
		t.Fatal("retry replaced immutable history")
	}
	again, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || !bytes.Equal(again.imageSelectionRaw, loaded.imageSelectionRaw) {
		t.Fatal("failed retry changed history", err)
	}
}

func TestSandboxImageHistoryAliasesRefuseInFullAndProjectedReaders(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	err := withHostStateReadContext(t.Context(), h, func(ctx context.Context) error {
		return withSandboxFile(ctx, h, false, func(file *sandboxFile) error {
			raw := file.sessions[p.SessionID]
			changed := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"Image_Selection_V1":{},"image_selection_v1":{}}`)...)
			if _, err := decodeSandboxContext(ctx, p.SessionID, changed, file.schema); err == nil {
				t.Fatal("full reader accepted history alias")
			}
			document, err := json.Marshal(map[string]any{"schema": file.schema, "host": h.Name, "sessions": map[string]json.RawMessage{p.SessionID: changed}})
			if err != nil {
				return err
			}
			if _, err := projectAgentSandboxPolicy(ctx, bytes.NewReader(document), int64(len(document)), h.Name, p.SessionID); err == nil {
				t.Fatal("projected reader accepted history alias")
			}

			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

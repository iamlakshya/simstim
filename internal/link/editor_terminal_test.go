package link

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestEditorTerminalModesRemainBinaryAcrossBothWireDeclarations(t *testing.T) {
	modes := []byte{128, 0, 0, 0x96, 0xff, 1, 0, 0, 0, 0xff, 0}
	header := proto.EditorChannelV1{Version: 1, Term: "xterm-256color", Cols: 80, Rows: 24, Modes: modes}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var decoded proto.EditorChannelV1
	if !api.ExactJSONFields(raw, decoded) || json.Unmarshal(raw, &decoded) != nil || !bytes.Equal(decoded.Modes, modes) {
		t.Fatal("binary SSH modes were replaced as invalid UTF-8", raw, decoded.Modes)
	}
	config := proto.EditorTerminalConfigV1{Term: decoded.Term, Cols: decoded.Cols, Rows: decoded.Rows, Modes: decoded.Modes}
	var carrier bytes.Buffer
	if err = proto.NewWriter(&carrier).WriteJSON(proto.TRequest, config); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadEditorFrameV1(&carrier, 8<<10)
	if err != nil {
		t.Fatal(err)
	}
	var guest proto.EditorTerminalConfigV1
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, guest) || json.Unmarshal(frame.Payload, &guest) != nil || !bytes.Equal(guest.Modes, modes) {
		t.Fatal("guest terminal configuration changed the original binary modes", guest.Modes, err)
	}
}

func TestEditorReadyHandoffDoesNotReadAheadIntoSFTP(t *testing.T) {
	var carrier bytes.Buffer
	if err := proto.NewWriter(&carrier).WriteJSON(proto.TResponse, proto.EditorReadyV1{Version: 1, Ready: true}); err != nil {
		t.Fatal(err)
	}
	raw := []byte{0, 0, 0, 5, 2, 0, 0, 0, 3, 0xff, 0x80}
	_, _ = carrier.Write(raw)
	if _, err := ReadEditorFrameV1(&carrier, 1024); err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(&carrier)
	if err != nil || !bytes.Equal(remaining, raw) {
		t.Fatal("ready framing consumed bytes from the raw SFTP subsystem", remaining, err)
	}
}

func TestEditorFrameRejectsOversizedDeclarationBeforeReadingBody(t *testing.T) {
	reader := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0x7f})
	if _, err := ReadEditorFrameV1(reader, 1024); err == nil {
		t.Fatal("an oversized guest frame entered body allocation")
	}
}

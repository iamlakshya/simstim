package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func progressItem(data []byte) item {
	sum := sha256.Sum256(data)
	return item{Kind: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestProgressCountsOnlyValidPositiveInOrderPayload(t *testing.T) {
	data := []byte("payload")
	declaration := progressItem(data)
	var wire bytes.Buffer
	if err := sendItem(t.Context(), proto.NewWriter(&wire), declaration, func(out io.Writer) error { _, err := out.Write(data); return err }); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := receiveItemProgress(t.Context(), bufio.NewReader(&wire), declaration, io.Discard, func(n int64) { count += n }); err != nil || count != int64(len(data)) {
		t.Fatalf("progress = %d, %v", count, err)
	}
	for _, invalid := range []struct {
		name string
		make func(*proto.Writer)
	}{
		{"zero chunk", func(w *proto.Writer) { _ = w.Write(proto.TUploadChunk, nil) }},
		{"oversized remaining", func(w *proto.Writer) { _ = w.Write(proto.TUploadChunk, append(data, 'x')) }},
		{"header repeat", func(w *proto.Writer) { _ = w.WriteJSON(proto.TUploadBegin, declaration) }},
		{"unparsed event", func(w *proto.Writer) { _ = w.WriteJSON(proto.TEvent, map[string]string{"event": "progress"}) }},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			var input bytes.Buffer
			writer := proto.NewWriter(&input)
			_ = writer.WriteJSON(proto.TUploadBegin, declaration)
			invalid.make(writer)
			count = 0
			if err := receiveItemProgress(t.Context(), bufio.NewReader(&input), declaration, io.Discard, func(n int64) { count += n }); err == nil || count != 0 {
				t.Fatalf("invalid activity renewed progress: %d %v", count, err)
			}
		})
	}
}

func TestFinalHashFailureDoesNotTurnChunkLivenessIntoSuccess(t *testing.T) {
	declaration := progressItem([]byte("expected"))
	var wire bytes.Buffer
	writer := proto.NewWriter(&wire)
	_ = writer.WriteJSON(proto.TUploadBegin, declaration)
	_ = writer.Write(proto.TUploadChunk, []byte("different")[:declaration.Size])
	_ = writer.WriteJSON(proto.TUploadEnd, itemEnd{SHA256: declaration.SHA256})
	var count int64
	err := receiveItemProgress(context.Background(), bufio.NewReader(&wire), declaration, io.Discard, func(n int64) { count += n })
	if err == nil || count != declaration.Size {
		t.Fatalf("liveness claimed a valid final hash: %d %v", count, err)
	}
}

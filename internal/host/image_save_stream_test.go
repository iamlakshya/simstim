package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
)

func TestSavedRootChunkStreamPreservesEveryByteAndItsOwnEnd(t *testing.T) {
	data := bytes.Repeat([]byte{0, 1, 2, 255, 'x'}, 40000)
	var frames bytes.Buffer
	writer := savedRootChunkWriter{output: &frames}
	if count, err := writer.Write(data); err != nil || count != len(data) {
		t.Fatal("root frame writer changed byte count", err)
	}
	if err := binary.Write(&frames, binary.LittleEndian, uint32(0)); err != nil {
		t.Fatal(err)
	}
	frames.WriteByte(0) // The guardian's later ACK belongs to its control reader.
	actual, err := io.ReadAll(&savedRootChunkReader{input: &frames})
	if err != nil || !bytes.Equal(actual, data) || frames.Len() != 1 {
		t.Fatal("framed root data changed or consumed its owner's ACK", err)
	}
	var invalid bytes.Buffer
	_ = binary.Write(&invalid, binary.LittleEndian, uint32(image.MaxChunk+1))
	if _, err := io.ReadAll(&savedRootChunkReader{input: &invalid}); err == nil {
		t.Fatal("an oversized frame entered the source pipe")
	}
	writer = savedRootChunkWriter{output: &frames, sent: image.SavedRootInputMax - 1}
	before := frames.Len()
	if _, err := writer.Write([]byte{1, 2}); err == nil || frames.Len() != before {
		t.Fatal("the aggregate raw root bound was checked after writes")
	}
}

func TestSavedRootEarlyGuardianFailureCancelsAndJoinsActualProducer(t *testing.T) {
	reader, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer input.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan imageFrameResult, 1)
	frames <- imageFrameResult{frame: imageGuardianFrame{Phase: "done", Failure: "fixture refusal"}}
	var ended atomic.Bool
	early, err := sendSavedRootStream(ctx, cancel, input, frames, func(ctx context.Context, _ io.Writer) error { <-ctx.Done(); ended.Store(true); return ctx.Err() })
	if err != nil || early == nil || early.frame.Failure != "fixture refusal" || !ended.Load() {
		t.Fatal("the original producer was not cancelled and joined before returning", err)
	}
}

func TestSavedRootConverterRoleHasNoSourceDescriptorGrant(t *testing.T) {
	if args, err := converterArguments(convertSavedRoot, 0, "owned.scope"); err != nil || len(args) != 3 || args[0] != "image-guardian" || args[1] != SavedRootConverterMode {
		t.Fatal("saved conversion did not reuse the canonical guardian", err)
	}
	if _, err := converterArguments(convertSavedRoot, 1, "owned.scope"); err == nil {
		t.Fatal("a source descriptor entered the saved root converter role")
	}
}

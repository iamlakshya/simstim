package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ReadSummary reads only the bounded metadata item through the SAME parser.
// The caller retains this exact buffered reader for following declarations.
// Decoding is not authority: validate caps, scope and the complete file-set
// digest before invoking a native recipe, Store or agent startup.
func ReadSummary(ctx context.Context, reader *bufio.Reader) (api.InputSummary, error) {
	return receiveSummary(ctx, reader)
}

// WriteSummary writes metadata in ordinary bounded stream chunks. It never
// attempts to put a multi-megabyte repository list in a ctl or opener frame.
func WriteSummary(ctx context.Context, out io.Writer, summary api.InputSummary) error {
	data, err := summaryBytes(summary)
	if err != nil {
		return err
	}
	return writeSummaryBytes(ctx, proto.NewWriter(out), data)
}

// EncodeSummary supplies a bounded trusted prefix for the fixed helper.
// Init may prepend it to the SAME buffered put reader after inspecting the
// summary itself, preserving already buffered declaration bytes.
func EncodeSummary(summary api.InputSummary) ([]byte, error) {
	var out bytes.Buffer
	if err := WriteSummary(context.Background(), &out, summary); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func summaryBytes(summary api.InputSummary) ([]byte, error) {
	data, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	if len(data)+1 > proto.MaxFrame {
		return nil, protocol("input repository metadata exceeds the ordinary request bound")
	}
	return data, nil
}

func writeSummaryBytes(ctx context.Context, out *proto.Writer, data []byte) error {
	sum := sha256.Sum256(data)
	declaration := item{Kind: "summary", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	return sendItem(ctx, out, declaration, func(target io.Writer) error {
		_, err := io.Copy(target, bytes.NewReader(data))
		return err
	})
}

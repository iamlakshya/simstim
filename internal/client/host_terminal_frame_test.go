package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// These generated facets test only frame grammar. They are not native owner
// captures and never reach a production deletion or checked-identity path.
func hostTerminalFrameFixture(t *testing.T, receipt []byte) hostTerminalFrame {
	t.Helper()
	h := hostTerminalHeader{Name: "box", Key: HostFileKey("box"), HostID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32),
		From: strings.Repeat("c", 32), Target: strings.Repeat("d", 32), ReceiptSize: int64(len(receipt)),
		OP:     hostTerminalFacet{Version: 1, Identity: "generated-op", Mode: 0600},
		Parent: hostTerminalFacet{Version: 1, Identity: "generated-parent", Mode: 0700, Directory: true}}
	frame, err := encodeHostTerminalFrame(h)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func hostTerminalFrameBytes(t *testing.T, frame hostTerminalFrame, raw []byte) []byte {
	t.Helper()
	stream := hostTerminalStream{ctx: t.Context(), frame: frame, receipt: bytes.NewReader(raw)}
	data := make([]byte, frame.total)
	if err := terminalReadAt(t.Context(), stream, data, 0); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHostTerminalFramePreservesUnboundedOpaqueReceiptAndUTCMetadata(t *testing.T) {
	raw := bytes.Repeat([]byte("{ \"future\": \"\\u0061\", \"n\": 1.2300 }\n"), 4096)
	frame := hostTerminalFrameFixture(t, raw)
	folder := hostTerminalFacet{Version: 1, Identity: "generated-folder", Mode: 0700, Directory: true}
	frame.header.Folder = &folder
	reference := cachefs.Reference{Version: 1, Identity: "generated-file", Mode: 0600, Size: 7, Modified: time.Unix(1700000000, 123).UTC()}
	frame.header.Data[3], frame.header.Locks[3], frame.header.Who = &reference, &reference, &reference
	frame, err := encodeHostTerminalFrame(frame.header)
	if err != nil {
		t.Fatal(err)
	}
	data := hostTerminalFrameBytes(t, frame, raw)
	parsed, err := readHostTerminalFrame(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil || parsed.total != frame.total || !bytes.Equal(parsed.prefix, frame.prefix) {
		t.Fatalf("canonical complete frame refused: %v", err)
	}
	got := make([]byte, parsed.header.ReceiptSize)
	if err := terminalReadAt(t.Context(), bytes.NewReader(data), got, parsed.receiptStart); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("opaque receipt bytes changed: %v", err)
	}
	if bytes.Contains(parsed.prefix, []byte(`"modified":"1970-01-01T00:00:00Z"`)) || !bytes.Contains(parsed.prefix, []byte(`"modified":"2023-11-14T22:13:20.000000123Z"`)) {
		t.Fatal("stable facets fabricated reference timestamps or regular metadata lost UTC")
	}
}

func TestHostTerminalFrameRejectsEnvelopeAliasesDuplicatesUnknownAndTrailing(t *testing.T) {
	raw := []byte("owner receipt remains opaque")
	frame := hostTerminalFrameFixture(t, raw)
	metadata := frame.prefix[len(hostTerminalPrefix)+9:]
	for _, tc := range []struct {
		name   string
		header []byte
		suffix string
	}{
		{"alias", bytes.Replace(metadata, []byte(`"name"`), []byte(`"Name"`), 1), hostTerminalFooter},
		{"duplicate", bytes.Replace(metadata, []byte(`"name":"box"`), []byte(`"name":"box","name":"box"`), 1), hostTerminalFooter},
		{"unknown", append([]byte(`{"future":true,`), metadata[1:]...), hostTerminalFooter},
		{"nested-unknown", bytes.Replace(metadata, []byte(`"identity":"generated-op"`), []byte(`"identity":"generated-op","future":true`), 1), hostTerminalFooter},
		{"native-version", bytes.Replace(metadata, []byte(`"version":1`), []byte(`"version":2`), 1), hostTerminalFooter},
		{"trailing-json", append(append([]byte(nil), metadata...), []byte(` {}`)...), hostTerminalFooter},
		{"footer", metadata, strings.Replace(hostTerminalFooter, "complete", "unknown!", 1)},
		{"trailing-frame", metadata, hostTerminalFooter + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := append([]byte(hostTerminalPrefix+terminalFixtureLength(tc.header)), tc.header...)
			mutated = append(mutated, raw...)
			mutated = append(mutated, tc.suffix...)
			if _, err := readHostTerminalFrame(t.Context(), bytes.NewReader(mutated), int64(len(mutated))); err == nil {
				t.Fatal("noncanonical envelope supplied completion authority")
			}
		})
	}
}

func terminalFixtureLength(metadata []byte) string {
	const digits = "0123456789abcdef"
	length := uint32(len(metadata))
	var out [9]byte
	for i := 7; i >= 0; i-- {
		out[i] = digits[length&15]
		length >>= 4
	}
	out[8] = '\n'
	return string(out[:])
}

func TestHostTerminalFrameChecksLengthsBeforeReceiptAccess(t *testing.T) {
	frame := hostTerminalFrameFixture(t, []byte("opaque"))
	frame.header.ReceiptSize = int64(^uint64(0) >> 1)
	if _, err := encodeHostTerminalFrame(frame.header); err == nil {
		t.Fatal("receipt arithmetic overflow was accepted")
	}
	for _, lead := range []string{"ffffffff\n", "00010001\n", "00000000\n", "0000000A\n"} {
		data := []byte(hostTerminalPrefix + lead)
		if _, err := readHostTerminalFrame(t.Context(), bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatal("unchecked metadata length was accepted")
		}
	}
	data := hostTerminalFrameBytes(t, hostTerminalFrameFixture(t, []byte("opaque")), []byte("opaque"))
	for _, size := range []int64{-1, int64(len(data)) - 1} {
		if _, err := readHostTerminalFrame(t.Context(), bytes.NewReader(data), size); err == nil {
			t.Fatal("invalid actual FD bounds were accepted")
		}
	}
}

func TestHostTerminalFrameEveryTornPrefixNeedsExactReconstruction(t *testing.T) {
	raw := []byte("unchanged original source receipt")
	frame := hostTerminalFrameFixture(t, raw)
	data := hostTerminalFrameBytes(t, frame, raw)
	stream := hostTerminalStream{ctx: t.Context(), frame: frame, receipt: bytes.NewReader(raw)}
	for length := 0; length < len(data); length++ {
		prefix := data[:length]
		if err := compareHostTerminalPrefix(t.Context(), bytes.NewReader(prefix), stream, int64(length)); err != nil {
			t.Fatalf("exact deterministic prefix %d refused: %v", length, err)
		}
		if _, err := readHostTerminalFrame(t.Context(), bytes.NewReader(prefix), int64(length)); !errors.Is(err, errHostTerminalIncomplete) {
			t.Fatalf("torn prefix %d granted completion or changed classification: %v", length, err)
		}
		if length != 0 {
			changed := append([]byte(nil), prefix...)
			changed[length-1] ^= 1
			if err := compareHostTerminalPrefix(t.Context(), bytes.NewReader(changed), stream, int64(length)); err == nil {
				t.Fatalf("mismatched prefix %d was adopted", length)
			}
		}
	}
}

func TestHostTerminalStreamContextOffsetsAndMissingSourcePreserveErrors(t *testing.T) {
	raw := []byte("original")
	frame := hostTerminalFrameFixture(t, raw)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stream := hostTerminalStream{ctx: ctx, frame: frame, receipt: bytes.NewReader(raw)}
	if _, err := stream.ReadAt(make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream lost cancellation identity: %v", err)
	}
	stream.ctx = t.Context()
	if _, err := stream.ReadAt(make([]byte, 1), -1); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("negative offset reached receipt: %v", err)
	}
	stream.receipt = bytes.NewReader(nil)
	if _, err := stream.ReadAt(make([]byte, 1), frame.receiptStart); !errors.Is(err, io.EOF) {
		t.Fatalf("missing source supplied reconstructed bytes: %v", err)
	}
}

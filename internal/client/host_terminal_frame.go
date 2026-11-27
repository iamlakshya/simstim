package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

const hostTerminalHeaderLimit = 64 << 10
const hostTerminalFooter = "\nhost-state-retirement/1 complete\n"

var errHostTerminalIncomplete = errors.New("the host retirement checkpoint is incomplete")

// Stable facets deliberately have no size or timestamp. They are produced
// from canonical native captures and compared with a fresh canonical capture.
// They never become a fabricated cachefs.Reference with invented metadata.
type hostTerminalFacet struct {
	Version   int    `json:"version"`
	Identity  string `json:"identity"`
	Mode      uint32 `json:"mode"`
	Directory bool   `json:"directory"`
}

type hostTerminalHeader struct {
	Name        string                `json:"name"`
	Key         string                `json:"key"`
	HostID      string                `json:"host_id"`
	Nonce       string                `json:"nonce"`
	From        string                `json:"from"`
	Target      string                `json:"target"`
	OP          hostTerminalFacet     `json:"op"`
	Parent      hostTerminalFacet     `json:"parent"`
	Folder      *hostTerminalFacet    `json:"folder"`
	Data        [4]*cachefs.Reference `json:"data"`
	Locks       [4]*cachefs.Reference `json:"locks"`
	Who         *cachefs.Reference    `json:"who"`
	ReceiptSize int64                 `json:"receipt_size"`
}

type hostTerminalFrame struct {
	header       hostTerminalHeader
	prefix       []byte
	receiptStart int64
	total        int64
}

func terminalFacet(reference cachefs.Reference) hostTerminalFacet {
	return hostTerminalFacet{Version: reference.Version, Identity: reference.Identity, Mode: reference.Mode, Directory: reference.Directory}
}

func (f hostTerminalFacet) valid(directory bool) bool {
	return f.Version == 1 && f.Identity != "" && f.Directory == directory && f.Mode&^uint32(0o777) == 0 &&
		(directory && f.Mode == 0o700 || !directory && f.Mode == 0o600)
}

func (h hostTerminalHeader) validate() error {
	proof := HostForgetMembershipProof{FromGeneration: h.From, TargetGeneration: h.Target}
	if !ValidHostName(h.Name) || h.Key != HostFileKey(h.Name) || !ValidHostID(h.HostID) || !ValidHostID(h.Nonce) ||
		!validHostForgetPair(proof) || !h.OP.valid(false) || !h.Parent.valid(true) || h.ReceiptSize <= 0 {
		return hostStateRemovalError("the terminal checkpoint has invalid identity or framing")
	}
	if h.Folder != nil && !h.Folder.valid(true) {
		return hostStateRemovalError("the terminal folder witness has another native role")
	}
	for i := range h.Data {
		if h.Folder == nil && (h.Data[i] != nil || h.Locks[i] != nil) || h.Data[i] != nil && h.Locks[i] == nil {
			return hostStateRemovalError("the terminal checkpoint has an incomplete state inventory")
		}
		if !terminalRegularKnown(h.Data[i]) || !terminalRegularKnown(h.Locks[i]) {
			return hostStateRemovalError("the terminal file witness is unknown")
		}
	}
	if !terminalRegularKnown(h.Who) {
		return hostStateRemovalError("the terminal diagnostic witness is unknown")
	}
	return nil
}

func terminalRegularKnown(r *cachefs.Reference) bool {
	if r == nil {
		return true
	}
	_, offset := r.Modified.Zone()
	return r.Known() && !r.Directory && r.Mode == 0o600 && offset == 0
}

func encodeHostTerminalFrame(header hostTerminalHeader) (hostTerminalFrame, error) {
	frame := hostTerminalFrame{header: header}
	if err := header.validate(); err != nil {
		return frame, err
	}
	metadata, err := json.Marshal(header)
	if err != nil {
		return frame, fmt.Errorf("encode terminal checkpoint: %w", err)
	}
	if len(metadata) > hostTerminalHeaderLimit {
		return frame, hostStateRemovalError("the generated terminal header exceeds its framing bound")
	}
	frame.prefix = []byte(fmt.Sprintf("%s%08x\n", hostTerminalPrefix, len(metadata)))
	frame.prefix = append(frame.prefix, metadata...)
	frame.receiptStart = int64(len(frame.prefix))
	// Subtract before adding the untrusted receipt length.
	const maxInt64 = int64(^uint64(0) >> 1)
	if header.ReceiptSize > maxInt64-frame.receiptStart-int64(len(hostTerminalFooter)) {
		return frame, hostStateRemovalError("the terminal receipt length overflows its frame")
	}
	frame.total = frame.receiptStart + header.ReceiptSize + int64(len(hostTerminalFooter))
	return frame, nil
}

func readHostTerminalFrame(ctx context.Context, source io.ReaderAt, size int64) (hostTerminalFrame, error) {
	var frame hostTerminalFrame
	if size < 0 {
		return frame, hostStateRemovalError("the terminal checkpoint has a negative size")
	}
	lead := make([]byte, len(hostTerminalPrefix)+9)
	n := int64(len(lead))
	if n > size {
		n = size
	}
	if err := terminalReadAt(ctx, source, lead[:n], 0); err != nil {
		return frame, err
	}
	known := n
	if known > int64(len(hostTerminalPrefix)) {
		known = int64(len(hostTerminalPrefix))
	}
	if !bytes.Equal(lead[:known], []byte(hostTerminalPrefix)[:known]) {
		return frame, hostStateRemovalError("the OP contains an unknown checkpoint format")
	}
	if n < int64(len(lead)) {
		return frame, errHostTerminalIncomplete
	}
	lengthBytes := lead[len(hostTerminalPrefix):]
	length, err := strconv.ParseUint(string(lengthBytes[:8]), 16, 32)
	if err != nil || length == 0 || length > hostTerminalHeaderLimit || lengthBytes[8] != '\n' ||
		string(lengthBytes[:8]) != fmt.Sprintf("%08x", length) {
		return frame, hostStateRemovalError("the terminal checkpoint header length is invalid")
	}
	if int64(length) > size-int64(len(lead)) {
		return frame, errHostTerminalIncomplete
	}
	metadata := make([]byte, int(length))
	if err := terminalReadAt(ctx, source, metadata, int64(len(lead))); err != nil {
		return frame, err
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame.header); err != nil {
		return frame, hostStateRemovalError("the terminal checkpoint header is unknown or malformed")
	}
	frame, err = encodeHostTerminalFrame(frame.header)
	if err != nil {
		return frame, err
	}
	// Exact canonical bytes reject aliases, duplicate fields, unknown fields,
	// alternate timestamps and trailing JSON without another JSON parser.
	if !bytes.Equal(frame.prefix, append(lead, metadata...)) {
		return frame, hostStateRemovalError("the terminal checkpoint header is not canonical")
	}
	if frame.total > size {
		return frame, errHostTerminalIncomplete
	}
	if frame.total != size {
		return frame, hostStateRemovalError("the terminal checkpoint has trailing bytes")
	}
	footer := make([]byte, len(hostTerminalFooter))
	if err := terminalReadAt(ctx, source, footer, size-int64(len(footer))); err != nil {
		return frame, err
	}
	if string(footer) != hostTerminalFooter {
		return frame, hostStateRemovalError("the terminal checkpoint has no valid completion footer")
	}
	return frame, ctx.Err()
}

func terminalReadAt(ctx context.Context, source io.ReaderAt, data []byte, offset int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := source.ReadAt(data, offset)
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil && !(errors.Is(err, io.EOF) && n == len(data)) {
		return err
	}
	if n != len(data) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

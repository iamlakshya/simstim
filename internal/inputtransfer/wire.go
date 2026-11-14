// Package inputtransfer carries a declared set of laptop inputs through the
// existing versioned KindPut stream and canonical proto frame parser.
package inputtransfer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Feature is required before fetching values or publishing input bytes.
const Feature = "session-inputs-v1"

// HelperMode is a closed current-helper mode, never an arbitrary command.
const HelperMode = "wall-input-v1"

// HelperApplyMode is a closed UID1000 mode that applies reviewed primary
// includes after the same Init owner has joined successful native clones.
const HelperApplyMode = "wall-input-apply-v1"

// CtlPrepare and CtlCancel belong to the existing canonical control reader.
const (
	CtlPrepare = "inputs.prepare"
	CtlCancel  = "inputs.cancel"
	FrameMax   = 64 << 10
)

// Scope binds a stream to one original session and immutable input phase.
// The actual owner additionally retains its current-run pointer and lifetime.
type Scope struct {
	Session string         `json:"session"`
	Nonce   api.InputNonce `json:"nonce"`
	Digest  string         `json:"digest"`
}

// Header is the only input opener. It carries no path or value authority.
type Header struct {
	Purpose string `json:"purpose"`
	Scope
}

// Control contains only trusted supervisor choices. The actual encoded ctl
// request must fit its existing64KiB bound before the helper is launched.
type Control struct {
	Scope
	CopyMax   int64    `json:"copy_max"`
	SeedMax   int64    `json:"seed_max"`
	Protected []string `json:"protected"`
}

// Stream retains the existing continuation's independent half-close.
type Stream interface {
	io.Reader
	io.Writer
	CloseWrite() error
	Close() error
}

type event struct {
	Event  string         `json:"event"`
	Index  int            `json:"index"`
	Nonce  api.InputNonce `json:"nonce"`
	Digest string         `json:"digest"`
	File   *api.InputFile `json:"file,omitempty"`
}

type item struct {
	Kind   string `json:"kind"`
	Index  int    `json:"index"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type itemEnd struct {
	Index  int    `json:"index"`
	SHA256 string `json:"sha256"`
}

func protocol(message string) error {
	return &proto.Error{Code: "input_protocol", Message: message, Hint: "Prepare the local inputs again with matching client and helper versions."}
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }

func (s Scope) validate() error {
	if !api.ValidSessionID(s.Session) || !s.Nonce.Valid() {
		return protocol("invalid session input scope")
	}
	// Summary validation supplies the same canonical digest shape.
	return (api.InputSummary{Version: api.SessionInputsV1, Nonce: s.Nonce, Digest: s.Digest}).Validate(api.DefaultCopyMax, api.DefaultSeedMax)
}

func readFrame(ctx context.Context, reader *bufio.Reader) (proto.Frame, error) {
	if err := ctx.Err(); err != nil {
		return proto.Frame{}, err
	}
	frame, err := proto.ReadLimit(reader, FrameMax)
	if err == nil && frame.Type == proto.TError {
		var failure proto.Error
		if json.Unmarshal(frame.Payload, &failure) != nil {
			return proto.Frame{}, protocol("invalid input failure")
		}
		return proto.Frame{}, &proto.Error{Code: api.CleanText(failure.Code, 80), Message: api.CleanBlock(failure.Message, api.BlockMax), Hint: api.CleanBlock(failure.Hint, api.BlockMax)}
	}
	return frame, err
}

func writeJSON(ctx context.Context, writer *proto.Writer, kind proto.Type, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data)+1 > FrameMax {
		return protocol("input control record exceeds its existing stream frame bound")
	}
	return writer.WriteCtx(ctx, kind, data)
}

func sendEvent(ctx context.Context, writer *proto.Writer, scope Scope, name string, index int, file *api.InputFile) error {
	return writeJSON(ctx, writer, proto.TEvent, event{Event: name, Index: index, Nonce: scope.Nonce, Digest: scope.Digest, File: file})
}

func expectEvent(ctx context.Context, reader *bufio.Reader, scope Scope, name string, index int) (event, error) {
	frame, err := readFrame(ctx, reader)
	if err != nil {
		return event{}, err
	}
	var message event
	if frame.Type != proto.TEvent || decodeEvent(frame.Payload, &message) != nil || message.Event != name || message.Index != index || message.Nonce != scope.Nonce || message.Digest != scope.Digest {
		return event{}, protocol("unexpected input phase or scope")
	}
	return message, nil
}

type chunkWriter struct {
	ctx       context.Context
	writer    *proto.Writer
	remaining int64
	hash      io.Writer
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, protocol("input source exceeded its sealed size")
	}
	written := 0
	for len(data) > 0 {
		count := min(len(data), FrameMax-1)
		if err := w.writer.WriteCtx(w.ctx, proto.TUploadChunk, data[:count]); err != nil {
			return written, err
		}
		_, _ = w.hash.Write(data[:count])
		w.remaining -= int64(count)
		written += count
		data = data[count:]
	}
	return written, nil
}

func sendItem(ctx context.Context, writer *proto.Writer, declaration item, write func(io.Writer) error) error {
	if err := writeJSON(ctx, writer, proto.TUploadBegin, declaration); err != nil {
		return err
	}
	sum := sha256.New()
	output := &chunkWriter{ctx: ctx, writer: writer, remaining: declaration.Size, hash: sum}
	if err := write(output); err != nil {
		return err
	}
	if output.remaining != 0 || hex.EncodeToString(sum.Sum(nil)) != declaration.SHA256 {
		return protocol("input source no longer matches the sealed declaration")
	}
	return writeJSON(ctx, writer, proto.TUploadEnd, itemEnd{Index: declaration.Index, SHA256: declaration.SHA256})
}

func receiveItem(ctx context.Context, reader *bufio.Reader, declaration item, output io.Writer) error {
	return receiveItemProgress(ctx, reader, declaration, output, nil)
}

func receiveItemProgress(ctx context.Context, reader *bufio.Reader, declaration item, output io.Writer, progress func(int64)) error {
	frame, err := readFrame(ctx, reader)
	if err != nil {
		return err
	}
	var begin item
	if frame.Type != proto.TUploadBegin || decodeItem(frame.Payload, &begin) != nil || begin != declaration {
		return protocol("input data does not match the expected declaration")
	}
	return receiveItemBodyProgress(ctx, reader, declaration, output, progress)
}

func receiveItemBody(ctx context.Context, reader *bufio.Reader, declaration item, output io.Writer) error {
	return receiveItemBodyProgress(ctx, reader, declaration, output, nil)
}

func receiveItemBodyProgress(ctx context.Context, reader *bufio.Reader, declaration item, output io.Writer, progress func(int64)) error {
	remaining, sum := declaration.Size, sha256.New()
	for {
		frame, err := readFrame(ctx, reader)
		if err != nil {
			return err
		}
		if frame.Type == proto.TUploadEnd {
			var end itemEnd
			if decodeEnd(frame.Payload, &end) != nil || remaining != 0 || end.Index != declaration.Index || end.SHA256 != declaration.SHA256 || hex.EncodeToString(sum.Sum(nil)) != declaration.SHA256 {
				return protocol("input ended without its exact size and hash")
			}
			return nil
		}
		if frame.Type != proto.TUploadChunk || len(frame.Payload) == 0 || int64(len(frame.Payload)) > remaining {
			return protocol("unexpected or excessive input data")
		}
		n, err := io.MultiWriter(output, sum).Write(frame.Payload)
		if err != nil {
			return fmt.Errorf("write declared input: %w", err)
		}
		if n != len(frame.Payload) {
			return io.ErrShortWrite
		}
		remaining -= int64(n)
		if progress != nil {
			progress(int64(n))
		}
	}
}

package inputtransfer

import (
	"bufio"
	"context"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Sources writes only the exact sealed local bytes. The client installs these
// callbacks after its canonical H5 gate, without handing local paths to peers.
type Sources struct {
	File       func(context.Context, api.InputFile, io.Writer) error
	Repository func(context.Context, api.InputRepository, io.Writer) error
	// OpenBundle opens the one seed-input KindBundle on the SAME dedicated
	// continuation. It is required for repositories, never for copied files.
	OpenBundle func(context.Context, Header) (Stream, error)
	// BeforePayload installs the canonical H5 first-write gate, once, after
	// ready and after a required bundle opener returns. The hook itself
	// holds no lock across a reply or long IO. Its returned writer must
	// use the actual stream Flush barrier before releasing first-write locks.
	// Pure codec callers grant nothing by omitting this producer-owned hook.
	BeforePayload func(context.Context, Stream) (Stream, error)
}

// Send transfers one immutable input set. The caller owns the stream and
// cancels/closes it when its operation ends, so blocked reads/writes unblock.
// H5 must gate this call at actual publication. This function grants nothing.
func Send(ctx context.Context, stream Stream, scope Scope, plan api.SessionInputs, sources Sources, copyMax, seedMax int64) (err error) {
	if stream == nil {
		return protocol("the canonical put stream is unavailable")
	}
	caller := ctx
	ctx, cancel := context.WithCancel(ctx)
	var replies *completion
	var mu sync.Mutex
	var bundle Stream
	closeOwned := func() {
		_ = stream.Close()
		mu.Lock()
		other := bundle
		mu.Unlock()
		if other != nil {
			_ = other.Close()
		}
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); closeOwned() })
	defer func() {
		if err != nil {
			cancel()
			closeOwned()
		}
		err = replies.join(caller, err)
		if !stop() {
			<-closed
		}
		cancel()
		if err != nil {
			closeOwned()
		}
	}()
	if err := scope.validate(); err != nil {
		return err
	}
	plan = plan.Canonical()
	summary, err := plan.Summary(copyMax, seedMax)
	if err != nil {
		return err
	}
	if summary.Nonce != scope.Nonce || summary.Digest != scope.Digest {
		return protocol("local input scope changed after admission")
	}
	if len(plan.Files) != 0 && sources.File == nil || len(plan.Repositories) != 0 && (sources.Repository == nil || sources.OpenBundle == nil) {
		return protocol("a sealed input source is unavailable")
	}
	metadata, err := summaryBytes(summary)
	if err != nil {
		return err
	}
	reader, writer := bufio.NewReader(stream), proto.NewWriter(stream)
	payloadEnded := make(chan struct{})
	count := len(plan.Files) + len(plan.Repositories)
	replies = readCompletion(ctx, cancel, reader, scope, count, payloadEnded, nil, nil)
	if err = writeSummaryBytes(ctx, writer, metadata); err != nil {
		return err
	}
	for index, file := range plan.Files {
		if err = sendEvent(ctx, writer, scope, "input.file", index, &file); err != nil {
			return err
		}
	}
	if err = sendEvent(ctx, writer, scope, "input.declarations_end", len(plan.Files), nil); err != nil {
		return err
	}
	if err = replies.waitReady(ctx); err != nil {
		return err
	}
	gated := false
	beforePayload := func(target Stream) (Stream, error) {
		if gated {
			return target, nil
		}
		gated = true
		if sources.BeforePayload == nil {
			return target, nil
		}
		wrapped, err := sources.BeforePayload(ctx, target)
		if err != nil {
			return nil, err
		}
		if wrapped == nil {
			return nil, protocol("the canonical input payload gate is unavailable")
		}
		return wrapped, nil
	}
	payloadPut := stream
	if len(plan.Files) != 0 || len(plan.Repositories) == 0 {
		payloadPut, err = beforePayload(stream)
		if err != nil {
			return err
		}
		writer = proto.NewWriter(payloadPut)
	}
	for index, file := range plan.Files {
		kind := "file"
		if file.Directory {
			kind = "directory"
		} else if file.Include {
			kind = "include"
		}
		declaration := item{Kind: kind, Index: index, Size: file.Size, SHA256: file.SHA256}
		if err = sendItem(ctx, writer, declaration, func(out io.Writer) error { return sources.File(ctx, file, out) }); err != nil {
			return err
		}
	}
	dataStream, dataWriter := payloadPut, writer
	var bundleReader *bufio.Reader
	if len(plan.Repositories) != 0 {
		// Copied data finishes on put. Seed bytes use the literal bundle
		// stream, while put remains the sole output completion channel.
		if err = stream.CloseWrite(); err != nil {
			return err
		}
		other, openErr := sources.OpenBundle(ctx, Header{Purpose: "inputs", Scope: scope})
		if openErr != nil {
			return openErr
		}
		if other == nil {
			return protocol("the canonical input bundle stream is unavailable")
		}
		mu.Lock()
		bundle = other
		mu.Unlock()
		defer other.Close()
		if err = ctx.Err(); err != nil {
			return err
		}
		payload, gateErr := beforePayload(other)
		if gateErr != nil {
			return gateErr
		}
		dataStream, dataWriter = payload, proto.NewWriter(payload)
		bundleReader = bufio.NewReader(other)
	}
	for index, repository := range plan.Repositories {
		declaration := item{Kind: "repository", Index: index, Size: repository.Size, SHA256: repository.SHA256}
		if err = sendItem(ctx, dataWriter, declaration, func(out io.Writer) error { return sources.Repository(ctx, repository, out) }); err != nil {
			return err
		}
	}
	if err = sendEvent(ctx, dataWriter, scope, "input.data_end", count, nil); err != nil {
		return err
	}
	close(payloadEnded)
	if err = dataStream.CloseWrite(); err != nil {
		return err
	}
	if err = replies.join(caller, nil); err != nil {
		return err
	}
	if bundleReader != nil {
		if _, err = readFrame(ctx, bundleReader); !isEOF(err) {
			return protocol("seed bundle stream omitted exact EOF")
		}
	}
	return nil
}

// Complete is called ONLY by the actual Init owner after helper exit/join and
// every owned native clone succeeds. The receipt is not independent authority.
func Complete(ctx context.Context, out io.Writer, scope Scope, count int) error {
	return sendEvent(ctx, proto.NewWriter(out), scope, "input.complete", count, nil)
}

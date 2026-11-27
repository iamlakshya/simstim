package inputtransfer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ForwardOptions belongs to the actual prestart session connection owner.
// Spool stores ONLY temporary declaration metadata and is removed by that
// owner after joining the operation. No copied file or bundle payload is kept.
type ForwardOptions struct {
	Scope
	Summary   api.InputSummary
	CopyMax   int64
	SeedMax   int64
	Protected []string
	Spool     io.ReadWriteSeeker
	// OpenGuest starts the actual owned receiver after full declarations
	// validate. It uses the existing controller/link/fixed helper owner.
	OpenGuest func(context.Context) (Stream, error)
	// Bundle openers are the SAME input continuation's one KindBundle
	// stream. Only the canonical phase/accept owner supplies them.
	OpenClientBundle func(context.Context, Header) (Stream, error)
	OpenGuestBundle  func(context.Context, Header) (Stream, error)
	// Audit records only a validated client declaration, before exposure.
	Audit func(api.InputFile) error
	// Progress accounts a complete validated in-order declaration row after
	// digest/spool, or a positive payload chunk after remaining/frame/hash
	// and forward checks. It conveys no final-hash success. The actual owner
	// validates current run/scope and applies fixed idle/absolute budgets.
	Progress func(int64)
	// SenderEOF runs once after the exact client data_end and EOF validate.
	// The owner disarms only sender-idle; the fixed absolute budget remains
	// active through native clones, cleanup and durable Finish.
	SenderEOF func()
	// Finish closes the owned host metadata spool and durably clears the
	// matching pending scope using the canonical writer. Client completion
	// is withheld until it succeeds. The actual consumer must supply it.
	Finish func(context.Context) error
}

// Forward validates the complete client manifest before opening its owned
// guest continuation. It reuses the same item codecs and never stores payload
// bytes, invokes Git, creates a process, reads values or publishes a session.
// The actual owner must join its helper/Git task before releasing resources.
func Forward(ctx context.Context, client Stream, options ForwardOptions) (err error) {
	if client == nil {
		return protocol("the authenticated input stream is unavailable")
	}
	if options.Spool == nil || options.OpenGuest == nil || options.Finish == nil {
		return protocol("the canonical input owner has no declaration spool or guest opener")
	}
	if len(options.Summary.Repositories) != 0 && (options.OpenClientBundle == nil || options.OpenGuestBundle == nil) {
		return protocol("the canonical seed bundle continuation is unavailable")
	}
	if err = options.Scope.validate(); err != nil {
		return err
	}
	if err = options.Summary.Validate(options.CopyMax, options.SeedMax); err != nil {
		return err
	}
	if options.Summary.Nonce != options.Nonce || options.Summary.Digest != options.Digest {
		return protocol("the protected input scope changed")
	}
	if err = ValidateProtected(options.Protected); err != nil {
		return err
	}
	metadata, err := summaryBytes(options.Summary)
	if err != nil {
		return err
	}
	end, err := options.Spool.Seek(0, io.SeekEnd)
	if err != nil || end != 0 {
		return protocol("the input declaration spool is not fresh")
	}
	if _, err = options.Spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	caller := ctx
	ctx, cancel := context.WithCancel(ctx)
	var replies *completion
	var mu sync.Mutex
	var guest, clientBundle, guestBundle Stream
	closeOwned := func() {
		_ = client.Close()
		mu.Lock()
		targets := []Stream{guest, clientBundle, guestBundle}
		mu.Unlock()
		for _, target := range targets {
			if target != nil {
				_ = target.Close()
			}
		}
	}
	closed := make(chan struct{})
	toClient := proto.NewWriter(client)
	stop := context.AfterFunc(ctx, func() { defer close(closed); closeOwned() })
	defer func() {
		if err != nil {
			_ = reportFailure(ctx, toClient, err)
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
	fromClient := bufio.NewReader(client)
	summary, err := receiveSummary(ctx, fromClient)
	if err != nil {
		return err
	}
	if !summary.Equal(options.Summary) {
		return protocol("streamed repository facts differ from the authenticated request")
	}
	receiveOptions := ReceiveOptions{Scope: options.Scope, CopyMax: options.CopyMax, SeedMax: options.SeedMax, Protected: options.Protected}
	hashes, err := receiveDeclarations(ctx, fromClient, options.Spool, receiveOptions, summary, options.Progress)
	if err != nil {
		return err
	}
	target, err := options.OpenGuest(ctx)
	if err != nil {
		return err
	}
	if target == nil {
		return protocol("the canonical guest input stream is unavailable")
	}
	defer target.Close()
	mu.Lock()
	guest = target
	mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	fromGuest, toGuest := bufio.NewReader(target), proto.NewWriter(target)
	payloadEnded := make(chan struct{})
	count := summary.FileCount + len(summary.Repositories)
	replies = readCompletion(ctx, cancel, fromGuest, options.Scope, count, payloadEnded,
		func(phase string) error {
			if phase == "input.staged" {
				return sendEvent(ctx, toClient, options.Scope, phase, count, nil)
			}
			return nil
		},
		func(cause error) { _ = reportFailure(ctx, toClient, cause) })
	if err = writeSummaryBytes(ctx, toGuest, metadata); err != nil {
		return err
	}
	each := func(fn func(int, api.InputFile) error) error {
		return eachDeclaration(ctx, options.Spool, options.Scope, hashes, fn)
	}
	if err = each(func(index int, file api.InputFile) error {
		return sendEvent(ctx, toGuest, options.Scope, "input.file", index, &file)
	}); err != nil {
		return err
	}
	if err = sendEvent(ctx, toGuest, options.Scope, "input.declarations_end", summary.FileCount, nil); err != nil {
		return err
	}
	if err = replies.waitReady(ctx); err != nil {
		return err
	}
	if err = sendEvent(ctx, toClient, options.Scope, "input.ready", 0, nil); err != nil {
		return err
	}
	if err = each(func(index int, file api.InputFile) error {
		if options.Audit != nil {
			if err := options.Audit(file); err != nil {
				return err
			}
		}
		kind := "file"
		if file.Directory {
			kind = "directory"
		} else if file.Include {
			kind = "include"
		}
		declaration := item{Kind: kind, Index: index, Size: file.Size, SHA256: file.SHA256}
		return sendItem(ctx, toGuest, declaration, func(out io.Writer) error {
			return receiveItemProgress(ctx, fromClient, declaration, out, options.Progress)
		})
	}); err != nil {
		return err
	}
	dataReader, dataWriter, dataStream := fromClient, toGuest, target
	var guestBundleReader *bufio.Reader
	if len(summary.Repositories) != 0 {
		if _, err = readFrame(ctx, fromClient); !isEOF(err) {
			return protocol("put input did not end before the seed bundle stream")
		}
		if err = target.CloseWrite(); err != nil {
			return err
		}
		header := Header{Purpose: "inputs", Scope: options.Scope}
		source, err := options.OpenClientBundle(ctx, header)
		if err != nil {
			return err
		}
		if source == nil {
			return protocol("the client seed bundle stream is unavailable")
		}
		mu.Lock()
		clientBundle = source
		mu.Unlock()
		defer source.Close()
		destination, err := options.OpenGuestBundle(ctx, header)
		if err != nil {
			return err
		}
		if destination == nil {
			return protocol("the guest seed bundle stream is unavailable")
		}
		mu.Lock()
		guestBundle = destination
		mu.Unlock()
		defer destination.Close()
		if err = ctx.Err(); err != nil {
			return err
		}
		dataReader, dataWriter, dataStream = bufio.NewReader(source), proto.NewWriter(destination), destination
		guestBundleReader = bufio.NewReader(destination)
	}
	for index, repository := range options.Summary.Repositories {
		declaration := item{Kind: "repository", Index: index, Size: repository.Size, SHA256: repository.SHA256}
		if err = sendItem(ctx, dataWriter, declaration, func(out io.Writer) error {
			return receiveItemProgress(ctx, dataReader, declaration, out, options.Progress)
		}); err != nil {
			return err
		}
	}
	if _, err = expectEvent(ctx, dataReader, options.Scope, "input.data_end", count); err != nil {
		return err
	}
	if _, err = readFrame(ctx, dataReader); !isEOF(err) {
		return protocol("input client sent trailing data or omitted EOF")
	}
	if options.SenderEOF != nil {
		options.SenderEOF()
	}
	if err = sendEvent(ctx, dataWriter, options.Scope, "input.data_end", count, nil); err != nil {
		return err
	}
	close(payloadEnded)
	if err = dataStream.CloseWrite(); err != nil {
		return err
	}
	if err = replies.join(caller, nil); err != nil {
		return err
	}
	if guestBundleReader != nil {
		if _, err = readFrame(ctx, guestBundleReader); !isEOF(err) {
			return protocol("seed bundle continuation omitted exact EOF")
		}
	}
	if err = options.Finish(ctx); err != nil {
		return err
	}
	if err = sendEvent(ctx, toClient, options.Scope, "input.complete", count, nil); err != nil {
		return err
	}
	if clientBundle != nil {
		if err = clientBundle.CloseWrite(); err != nil {
			return err
		}
	}
	return client.CloseWrite()
}

func eachDeclaration(ctx context.Context, spool io.ReadSeeker, scope Scope, hashes [][32]byte, fn func(int, api.InputFile) error) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(spool)
	for index, expected := range hashes {
		message, err := expectEvent(ctx, reader, scope, "input.file", index)
		if err != nil {
			return err
		}
		data, err := json.Marshal(*message.File)
		if err != nil || sha256.Sum256(data) != expected {
			return protocol("the declaration spool changed")
		}
		if err = fn(index, *message.File); err != nil {
			return err
		}
	}
	if _, err := readFrame(ctx, reader); !isEOF(err) {
		return protocol("the declaration spool has trailing data")
	}
	return nil
}

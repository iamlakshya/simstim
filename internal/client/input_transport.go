package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// New waits for inputs during boot. Start the sender only after the actual
// original request frame, and join it before the frontend can close R or Conn.
func (c *Conn) callNewWithInputs(base, publication context.Context, a NewArgs, out *Session, release func()) error {
	p := newInputPreparationOf(base)
	if p == nil || a.Inputs == nil {
		return c.callWithSent(publication, "new", a, out, release)
	}
	run, cancel := context.WithCancel(base)
	defer cancel()
	// Keep the original publication witness while giving its await the SAME
	// cancellation source as the input owner. No helper invents another budget.
	publication, stop := context.WithCancel(publication)
	stopRun := context.AfterFunc(run, stop)
	defer func() { stopRun(); stop() }()
	id, response, err := c.startCall(publication, "new", a)
	release()
	if err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() {
		err := p.sendNewInputs(run, c, a)
		if err != nil {
			cancel()
		}
		finished <- err
	}()
	answer := c.awaitCall(publication, "new", id, response, out)
	if answer != nil {
		cancel()
	}
	inputErr := <-finished
	return errors.Join(answer, inputErr)
}

// The raw child must inherit the accepted parent's ORIGINAL Source before
// SSH starts. The subsequent physical begin frame has its own short OP; no
// HostLock is held across child startup, readiness, bundle opens or bulk IO.
func dialInputContinuation(ctx context.Context, parent *Conn) (*Conn, error) {
	raw, err := dialHostContinuation(withInputContinuationDial(WithoutWake(ctx)), parent)
	if err != nil {
		return nil, err
	}
	if err := checkInputContinuationPair(parent, raw); err != nil {
		raw.execReadFinished()
		raw.Close()
		return nil, err
	}
	return raw, nil
}

func checkInputContinuationPair(parent, raw *Conn) error {
	if parent == nil || raw == nil || parent == raw || parent.agentOnly || raw.agentOnly || raw.execReader == nil ||
		!ValidHostID(parent.HostID()) || parent.HostID() != raw.HostID() || parent.HostIDChanged() || raw.HostIDChanged() {
		return sandboxReceiptError()
	}
	if err := checkAcceptedHostTransport(parent); err != nil {
		return err
	}
	if err := checkAcceptedHostTransport(raw); err != nil {
		return err
	}
	if !sameHostTransportSource(parent.hostTransport.source, raw.hostTransport.source) {
		return grantIdentityError(parent.Host)
	}
	return nil
}

// The ONE request encoder/physical writer is shared with ordinary RPCs. Only
// the dedicated raw owner reads this exact response; it never runs readLoop.
func inputContinuationRequest(ctx context.Context, parent, raw *Conn, op string, args, result any) (uint64, hostStateSourceWitness, error) {
	var source hostStateSourceWitness
	owned, release, err := parent.beginGrant(ctx, "input continuation", true)
	if err != nil {
		return 0, source, err
	}
	if err = checkInputContinuationPair(parent, raw); err == nil {
		err = checkTransportBeforeTierMigration(owned, raw)
	}
	if err == nil {
		source, err = captureHostStateSourceWitness(owned, parent)
	}
	var id uint64
	if err == nil {
		id, _, err = raw.startCall(owned, op, args)
	}
	release()
	if err != nil {
		return id, source, err
	}
	defer raw.drop(id)
	frame, err := proto.Read(raw.execReader)
	if err != nil {
		return id, source, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, proto.Response{}) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
		return id, source, sandboxReceiptError()
	}
	if !response.OK {
		if response.Error != nil {
			return id, source, hostErrorOn(parent.Host.Name, response.Error)
		}
		return id, source, sandboxReceiptError()
	}
	if len(response.Result) == 0 || !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, result) != nil {
		return id, source, sandboxReceiptError()
	}
	return id, source, ctx.Err()
}

type inputPublicationWitness struct {
	mu                         sync.Mutex
	parent, raw                *Conn
	preparation                *newInputPreparation
	creation                   *SandboxPolicy
	source                     hostStateSourceWitness
	scope                      inputtransfer.Scope
	summary                    api.InputSummary
	beginID                    uint64
	begin                      inputtransfer.BeginResult
	link                       *link.Session
	receipt                    *sandboxInputOperation
	persistAttempted, finished bool
}

func (p *newInputPreparation) sendNewInputs(ctx context.Context, c *Conn, args NewArgs) (returned error) {
	p.mu.Lock()
	if p.closed || p.busy || !p.ready || !p.payload || p.plan == nil || args.Inputs == nil || !args.Inputs.Equal(p.plan.summary) {
		p.mu.Unlock()
		return grantOperationError()
	}
	plan := p.plan
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	secrets := newPlanOf(ctx)
	if secrets == nil || secrets.conn != c || secrets.creation == nil || args.ID != secrets.creation.SessionID {
		return grantOperationError()
	}
	w := &inputPublicationWitness{parent: c, preparation: p, creation: cloneSandboxPolicy(secrets.creation), summary: args.Inputs.Clone(),
		scope: inputtransfer.Scope{Session: args.ID, Nonce: args.Inputs.Nonce, Digest: args.Inputs.Digest}}
	raw, err := dialInputContinuation(ctx, c)
	if err != nil {
		return err
	}
	w.raw = raw
	defer raw.Close()
	defer raw.execReadFinished()
	carrier, err := newInputRawCarrier(raw)
	if err != nil {
		raw.kill()
		return err
	}
	defer carrier.Close()
	// Closing the owned carrier on caller cancellation interrupts its exact
	// raw read/write owner. Stop and join this callback before Conn.Close.
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = carrier.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	w.beginID, w.source, err = inputContinuationRequest(ctx, c, raw, "inputs.begin", inputtransfer.BeginArgs{Scope: w.scope}, &w.begin)
	if err != nil {
		return err
	}
	if w.beginID == 0 || w.begin.Version != 1 || w.begin.Finished || !ValidHostID(w.begin.RunGeneration) {
		return sandboxReceiptError()
	}
	w.link = link.ContinueInputs(carrier, raw.execReader, link.Guest, len(w.summary.Repositories) != 0)
	defer func() { returned = errors.Join(returned, w.link.Close()) }()
	put, err := w.link.Open(ctx, link.KindPut, inputtransfer.Header{Purpose: "inputs", Scope: w.scope})
	if err != nil {
		return err
	}
	defer put.Close()
	err = plan.withSources(ctx, func(declaration api.SessionInputs, sources inputtransfer.Sources) error {
		validate := sources.ValidatePayload
		sources.ValidatePayload = func(ctx context.Context) error {
			if validate != nil {
				if err := validate(ctx); err != nil {
					return err
				}
			}
			return p.revalidateInputFacts(ctx)
		}
		sources.OpenBundle = func(ctx context.Context, header inputtransfer.Header) (inputtransfer.Stream, error) {
			if header.Purpose != "inputs" || header.Scope != w.scope {
				return nil, sandboxReceiptError()
			}
			return w.link.Open(ctx, link.KindBundle, header)
		}
		sources.BeforePayload = w.beforePayload
		return inputtransfer.Send(ctx, put, w.scope, declaration, sources, plan.copyMax, plan.seedMax)
	})
	if err != nil {
		return err
	}
	// Send succeeds only after the exact current stream's finished event and
	// EOF. Its BeginResult run and private original writer bind that ACK.
	w.mu.Lock()
	w.finished = true
	receipt := cloneSandboxInputOperation(w.receipt)
	w.mu.Unlock()
	if receipt == nil {
		return sandboxReceiptError()
	}
	return clearSandboxInputOperationContext(ctx, c, w.creation, receipt, w)
}

func (w *inputPublicationWitness) beforePayload(ctx context.Context, stream inputtransfer.Stream) (inputtransfer.Stream, error) {
	raw, ok := stream.(*link.Stream)
	if !ok || raw == nil || w.link == nil || w.beginID == 0 {
		return nil, sandboxReceiptError()
	}
	return &inputFirstPayload{Stream: raw, witness: w, ctx: ctx}, nil
}

// This owns the actual raw Stream.Write AND Flush. A wrapper Flush method
// without the original raw link barrier cannot release first-write permission.
type inputFirstPayload struct {
	*link.Stream
	mu      sync.Mutex
	witness *inputPublicationWitness
	ctx     context.Context
	done    bool
	err     error
}

func (s *inputFirstPayload) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if s.done || len(data) == 0 {
		return s.Stream.Write(data)
	}
	w := s.witness
	owned, release, err := w.parent.beginGrant(s.ctx, "publish laptop inputs", true)
	if err != nil {
		s.err = err
		return 0, err
	}
	defer release()
	if err := w.prepareReceipt(owned); err != nil {
		s.err = err
		return 0, err
	}
	n, err := s.Stream.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.Stream.Flush(s.ctx)
	}
	if err != nil {
		// Flush cancellation alone leaves queued/in-flight bytes alive.
		// Revoke the owned carrier before releasing its publication OP.
		err = errors.Join(err, w.link.Close())
		s.err = err
		return n, err
	}
	s.done = true
	return n, nil
}

func (w *inputPublicationWitness) prepareReceipt(ctx context.Context) error {
	if err := w.preparation.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := checkInputContinuationPair(w.parent, w.raw); err != nil {
		return err
	}
	if err := w.source.check(ctx, w.parent); err != nil {
		return err
	}
	if err := w.preparation.allowInputFacts(ctx, true, w.creation.Ceiling); err != nil {
		return err
	}
	revision, err := newLocalReceiptID()
	if err != nil {
		return err
	}
	hash, err := sandboxInputSummaryHash(ctx, w.summary, w.preparation.plan.copyMax, w.preparation.plan.seedMax)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.persistAttempted || w.receipt != nil || w.preparation.pin == nil {
		w.mu.Unlock()
		return sandboxReceiptError()
	}
	w.receipt = &sandboxInputOperation{Version: 1, Revision: revision, Creation: w.creation.request, Operation: "new",
		Scope: sandboxReceiptScope{Host: w.parent.Host.Name, Source: w.source.clone(), HostID: w.parent.HostID(), MemberID: w.scope.Session,
			RootID: localSandboxRoot(w.creation), RunGeneration: w.begin.RunGeneration},
		Nonce: w.scope.Nonce, Digest: w.scope.Digest, SummaryHash: hash, SelectionHash: w.preparation.pin.Digest}
	w.persistAttempted = true
	receipt := cloneSandboxInputOperation(w.receipt)
	w.mu.Unlock()
	return storeSandboxInputOperationContext(ctx, w.parent, w.creation, "", receipt, w)
}

func (w *inputPublicationWitness) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	w.mu.Lock()
	valid := intent != nil && intent.conn == w.parent && w.beginID != 0 && w.begin.Version == 1 && !w.begin.Finished &&
		ValidHostID(w.begin.RunGeneration) && w.persistAttempted && w.receipt != nil && sameSandboxCreation(intent.original, w.creation) &&
		reflect.DeepEqual(intent.input, w.receipt) && (intent.kind == inputOperationBegin && intent.expectedRevision == "" && !w.finished ||
		intent.kind == inputOperationFinish && w.finished && intent.expectedRevision == w.receipt.Revision)
	w.mu.Unlock()
	if !valid {
		return sandboxReceiptError()
	}
	if err := w.preparation.operation.Revalidate(ctx); err != nil {
		return err
	}
	return w.source.check(ctx, w.parent)
}

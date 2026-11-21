package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// originalStartup retains the ONE accepted physical line. The native host
// owner issues the operation; this consumer cannot manufacture a replacement.
type originalStartup struct {
	mu            sync.Mutex
	line          *host.StartupLineV1
	values        map[string]string
	readDone      chan struct{}
	cancelRequest uint64
	ready         bool
}

// awaitOriginalLoan runs after the holder lease and socket exist, before any
// runtime factory, agent pin, mount or guest is allowed to start.
func (s *Supervisor) awaitOriginalLoan(ctx context.Context) (*host.SessionOperation, error) {
	listener, ok := s.ln.(*net.UnixListener)
	if !ok || s.bootOwner == nil {
		return nil, execRefusal("the startup has no original native listener and supervisor owner")
	}
	deadline := time.Now().Add(secretsWait)
	_ = listener.SetDeadline(deadline)
	defer listener.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = listener.SetDeadline(time.Now()) })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return nil, wallError("the original outside startup loan did not arrive", err)
		}
		operation, _, err := s.readOriginalLoan(ctx, conn, deadline)
		if errors.Is(err, errEventObserverAccepted) {
			continue
		}
		if operation != nil {
			// An accepted physical loan is custody even when a subsequent
			// capture or reply fails. Never resume accepting a replacement.
			return operation, err
		}
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func (s *Supervisor) readOriginalLoan(ctx context.Context, conn net.Conn, deadline time.Time) (*host.SessionOperation, *host.StartupLineV1, error) {
	line, err := host.NewNativeStartupLineV1(conn)
	if err != nil {
		return nil, nil, err
	}
	retained := false
	defer func() {
		if !retained {
			_ = line.Close()
		}
	}()
	_ = line.SetDeadline(minTime(deadline, time.Now().Add(3*time.Second)))
	frame, err := line.ReadFrame()
	if err != nil {
		return nil, nil, err
	}
	var hello proto.Hello
	if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil {
		return nil, nil, execRefusal("startup requires an exact compatible hello")
	}
	if !host.FullPeer(conn, s.id, s.peerCheck) {
		return nil, nil, host.ErrInside
	}
	nativeHello := hello.Role == host.NativeStartupRoleV1
	if nativeHello {
		if err := line.AcceptNativeHelloV1(frame); err != nil {
			return nil, nil, err
		}
	} else {
		if err := line.RejectAncillary(); err != nil {
			return nil, nil, err
		}
	}
	nonce := ""
	if nativeHello {
		nonce, err = s.bootOwner.PrepareStartupHello(ctx, line)
	}
	if err != nil {
		return nil, nil, err
	}
	responseHello := proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor", Features: Features, StartupNonce: nonce}
	if nativeHello {
		err = line.WriteNativeHelloV1(responseHello)
	} else {
		err = line.WriteFrame(proto.THello, responseHello)
	}
	if err != nil {
		return nil, nil, err
	}
	frame, err = line.ReadFrame()
	if err != nil {
		return nil, nil, err
	}
	var observerRequest proto.Request
	if frame.Type == proto.TRequest && api.ExactJSONFields(frame.Payload, proto.Request{}) && json.Unmarshal(frame.Payload, &observerRequest) == nil && observerRequest.ID != 0 && observerRequest.Op == "events.watch" {
		if err = line.RejectAncillary(); err != nil {
			return nil, nil, err
		}
		if err = s.startEventObserver(conn, line, observerRequest); err != nil {
			return nil, nil, err
		}
		retained = true
		return nil, nil, errEventObserverAccepted
	}
	if !nativeHello {
		return nil, nil, execRefusal("private startup needs the original native code and namespace hello")
	}
	var operation *host.SessionOperation
	if observerRequest.Op == proto.NativeSlotReservedStartupPrepareV2 {
		if err := s.bootOwner.PrepareSlotReservedStartupV2(ctx, line, frame); err != nil {
			return nil, nil, err
		}
		frame, err = line.ReadFrame()
		if err != nil {
			return nil, nil, err
		}
		operation, err = s.bootOwner.AcceptSlotReservedStartupV2(ctx, line, frame)
	} else {
		operation, err = s.bootOwner.AcceptStartupLoan(ctx, line, frame)
	}
	if operation != nil {
		retained = true
		s.bootOperation = operation
		s.originalStart = &originalStartup{line: line, readDone: make(chan struct{})}
	}
	if err != nil {
		return operation, line, err
	}
	var request proto.Request
	if json.Unmarshal(frame.Payload, &request) != nil {
		return operation, line, execRefusal("the accepted loan request lost its reply identity")
	}
	refuseAccepted := func(failure error) (*host.SessionOperation, *host.StartupLineV1, error) {
		failure = errors.Join(failure, line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, Error: toProtoErr(failure)}))
		return operation, line, failure
	}
	// Capture the SAME accepted placement and native parent before any
	// factory. Adopt nonnil partial results before interpreting errors.
	s.bootPlacementCleanup, err = s.bootOwner.PlacementCleanupV1(operation)
	if err != nil {
		return refuseAccepted(err)
	}
	if operation.IsSandboxPrepareBootV1() {
		s.prepareParentCleanup, err = s.bootOwner.CaptureSandboxPrepareParentCleanupV1(operation)
		if err != nil {
			return refuseAccepted(err)
		}
	}
	if err = s.bootOwner.ReplyStartupLoan(line, request.ID, operation); err != nil {
		s.cancelOriginalStartup(err)
		return operation, line, err
	}
	retained = true
	return operation, line, nil
}

// receiveOriginalStart always consumes the selected envelope on the SAME loan
// line, including a boot with no secret values or automatic allow-missing.
func (s *Supervisor) receiveOriginalStart(ctx context.Context) error {
	start := s.originalStart
	if start == nil || start.line == nil {
		return execRefusal("startup has no accepted original loan line")
	}
	_ = start.line.SetDeadline(time.Now().Add(secretsWait))
	stop := context.AfterFunc(ctx, func() { _ = start.line.SetDeadline(time.Now()) })
	frame, err := start.line.ReadFrame()
	stop()
	if err != nil {
		return err
	}
	if err = s.bootOwner.CheckStartupFrame(start.line); err != nil {
		return err
	}
	var request proto.Request
	if frame.Type == proto.TRequest && api.ExactJSONFields(frame.Payload, request) && json.Unmarshal(frame.Payload, &request) == nil &&
		request.ID == 4 && request.Op == "cancel_start" && len(request.Args) == 0 && s.isSandboxPrepareBootV1() {
		if err := s.bootOwner.CheckSandboxPrepareCancelV1(start.line); err != nil {
			return err
		}
		start.mu.Lock()
		start.cancelRequest = request.ID
		start.mu.Unlock()
		s.cancelOriginalStartup(errStopped)
		return errStopped
	}
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, proto.Request{}) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 || request.Op != "secrets" {
		return execRefusal("the original loan needs its exact selected start envelope")
	}
	defer clear(frame.Payload)
	defer clear(request.Args)
	values, err := s.validateOriginalStartupSecretsV1(ctx, start, request.Args, s.m.Secrets)
	request.Args = nil
	if err != nil {
		_ = start.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, Error: toProtoErr(err)})
		return err
	}
	if err = start.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true}); err != nil {
		return err
	}
	start.values = values
	_ = start.line.SetDeadline(time.Time{})
	s.originalReaderStarted = true
	go s.readOriginalControl(start)
	return nil
}

// readOriginalControl keeps the sole reader active during boot. Ready only
// registers a reply; cancellation can still revoke and join the actual boot.
func (s *Supervisor) readOriginalControl(start *originalStartup) {
	defer close(start.readDone)
	for {
		frame, err := start.line.ReadFrame()
		if err != nil {
			ready := s.bootOwner.StartupCommittedV1()
			if !ready || s.isSandboxPrepareBootV1() {
				s.cancelOriginalStartup(err)
			}
			return
		}
		if err = start.line.RejectAncillary(); err != nil {
			s.cancelOriginalStartup(err)
			return
		}
		var request proto.Request
		if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, proto.Request{}) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 || len(request.Args) != 0 {
			s.cancelOriginalStartup(execRefusal("the original startup control request changed"))
			return
		}
		if s.isSandboxPrepareBootV1() && !((request.Op == host.StartupReadyOperationV1 && request.ID == 3) ||
			(request.Op == host.PolicyNextCommitOperationV1 && request.ID == 5) || (request.Op == "cancel_start" && request.ID == 4)) {
			s.cancelOriginalStartup(execRefusal("the original preparation control request changed its fixed identity"))
			return
		}
		switch request.Op {
		case host.StartupReadyOperationV1:
			if err = s.bootOwner.RegisterStartupReady(start.line, request.ID); err != nil {
				s.cancelOriginalStartup(err)
				return
			}
		case host.PolicyNextCommitOperationV1:
			if err = s.commitOriginalPolicyNextV1(start.line, request.ID); err != nil {
				s.log.Warn("the original pending policy metadata commit failed", "err", err)
				return
			}
		case "cancel_start":
			ready := s.bootOwner.StartupCommittedV1()
			if ready && !s.isSandboxPrepareBootV1() {
				_ = start.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, Error: toProtoErr(execRefusal("the original startup is already durably ready"))})
				return
			}
			if s.isSandboxPrepareBootV1() {
				err = s.bootOwner.CheckSandboxPrepareCancelV1(start.line)
			} else {
				err = s.bootOwner.CheckStartupFrame(start.line)
			}
			if err != nil {
				s.cancelOriginalStartup(err)
				return
			}
			start.mu.Lock()
			start.cancelRequest = request.ID
			start.mu.Unlock()
			s.cancelOriginalStartup(errStopped)
			return // ACK waits for final resource/borrowed-OP/holder cleanup.
		default:
			s.cancelOriginalStartup(execRefusal("the original startup control operation is unsupported"))
			return
		}
	}
}

func (s *Supervisor) cancelOriginalStartup(err error) {
	if s.bootOwner != nil && s.bootOwner.StartupCommittedV1() && !s.isSandboxPrepareBootV1() {
		return
	}
	s.markStopIntent()
	if s.cancelLifetime != nil {
		s.cancelLifetime(err)
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func (s *Supervisor) commitOriginalReady() error {
	if err := s.reconcilePorts(); err != nil {
		return err
	}
	start := s.originalStart
	if start == nil {
		return nil
	} // The explicit plain test driver has no outside issuer.
	if err := s.publishNativeAccountReadyV1(s.lifetimeContext()); err != nil {
		return err
	}
	err := s.bootOwner.CommitStartupReady(s.lifetimeContext(), s.bootOperation)
	if !s.bootOwner.StartupCommittedV1() {
		if err == nil {
			return execRefusal("the original native startup did not commit readiness")
		}
		return err
	}
	start.mu.Lock()
	start.ready = true
	start.mu.Unlock()
	if err != nil {
		s.log.Warn("the committed original startup could not acknowledge its outside parent", "err", err)
	}
	return nil
}

// finishOriginalStartup retires only after the SAME runtime, resources and
// original placement have joined. A missing ACK leaves the actual owners here.
func (s *Supervisor) finishOriginalStartup(cleanup error) error {
	prepare := s.isSandboxPrepareBootV1()
	cleanup = errors.Join(cleanup, s.closeEventObservers())
	if s.unwalledV1() && cleanup != nil && s.sandboxStartAttempted {
		if runtime, ok := s.sb.(noneRuntime); ok && !runtime.preparedCleanupConfirmedV1() {
			// Keep the SAME original actor and holder after an uncertain join.
			return cleanup
		}
	}
	if prepare {
		for cleanup != nil {
			s.logPrepareCleanupV1(cleanup)
			time.Sleep(100 * time.Millisecond)
			cleanup = errors.Join(s.closeSandbox(), s.closeEventObservers())
		}
	}
	start := s.originalStart
	var request uint64
	if prepare && start != nil {
		// Natural /true exit cannot retire the original reader or OP while
		// its outside owner may still have an earlier reply queued.
		start.mu.Lock()
		request = start.cancelRequest
		start.mu.Unlock()
		if !s.originalReaderStarted && request == 0 {
			// A pre-start failure still owes queued native replies and the
			// authentic cancel on the SAME cursor, without starting a runtime.
			s.originalReaderStarted = true
			_ = start.line.SetDeadline(time.Time{})
			go s.readFailedOriginalControlV1(start)
		}
		if s.originalReaderStarted {
			<-start.readDone
		}
		start.mu.Lock()
		request = start.cancelRequest
		start.mu.Unlock()
	}
	// Use a fresh cleanup context, never the cancelled construction budget.
	// This token drains its captured placement, never a later global entry.
	if s.bootPlacementCleanup != nil {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := s.bootPlacementCleanup.CloseContextV1(ctx)
			cancel()
			if err == nil {
				break
			}
			if !prepare {
				return errors.Join(cleanup, err)
			}
			s.logPrepareCleanupV1(err)
			time.Sleep(100 * time.Millisecond)
		}
	}
	if prepare {
		// With no authenticated cancel, EOF or an invalid frame supplies no
		// closure proof. Retain these SAME owners until the original pidfd
		// positively exits and anchored removal succeeds. Live cancel leaves
		// the original outside owner responsible for its directory.
		for {
			if s.prepareParentCleanup == nil {
				s.logPrepareCleanupV1(execRefusal("fixed test cleanup has no captured original parent"))
			} else if removed, err := s.prepareParentCleanup.RemoveAfterParentExitV1(); err != nil {
				s.logPrepareCleanupV1(err)
			} else if removed {
				// The SAME outside parent is positively dead and its anchored
				// metadata is gone. There is no surviving ACK audience.
				request = 0
				break
			} else if request != 0 {
				if err := s.bootOwner.ReplySandboxPrepareCancelledReadyV1(start.line); err != nil {
					// A lost response cannot be turned into a cleanup ACK.
					request = 0
					s.logPrepareCleanupV1(err)
				} else if err := s.prepareParentCleanup.Close(); err != nil {
					s.logPrepareCleanupV1(err)
				} else {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if s.bootOperation != nil {
		cleanup = errors.Join(cleanup, s.bootOperation.CloseWithError())
	}
	if s.bootOwner != nil && !(s.unwalledV1() && s.bootOwner.StartupCommittedV1()) {
		cleanup = errors.Join(cleanup, s.bootOwner.CloseWithError())
	}
	// Native descriptor Close failures are sticky. Keep the original stack;
	// a second no-op Close or process exit cannot certify successful cleanup.
	for prepare && cleanup != nil {
		s.logPrepareCleanupV1(cleanup)
		time.Sleep(100 * time.Millisecond)
	}
	if start == nil {
		return cleanup
	}
	if !prepare {
		start.mu.Lock()
		request = start.cancelRequest
		start.mu.Unlock()
	}
	if request != 0 && cleanup == nil {
		cleanup = start.line.WriteFrame(proto.TResponse, proto.Response{ID: request, OK: true})
	}
	cleanup = errors.Join(cleanup, start.line.Close())
	for prepare && cleanup != nil {
		// Missing native ACK is a retained failure, never successful cleanup.
		s.logPrepareCleanupV1(cleanup)
		time.Sleep(100 * time.Millisecond)
	}
	if s.originalReaderStarted {
		<-start.readDone
	}
	return cleanup
}

func (s *Supervisor) logPrepareCleanupV1(err error) {
	if s.log != nil {
		s.log.Warn("fixed test retains its original cleanup owner", "err", err)
	}
}

func (s *Supervisor) closeCommittedStartupReaderV1() error {
	if s.isSandboxPrepareBootV1() {
		return nil // The fixed test retains its SAME original cancellation cursor.
	}
	start := s.originalStart
	if start == nil || start.line == nil || s.bootOwner == nil || !s.bootOwner.StartupCommittedV1() {
		return nil
	}
	err := start.line.Close()
	if s.originalReaderStarted {
		<-start.readDone
	}
	return err
}

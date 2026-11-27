package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

// This is the actual creator's N5 completion, never a whole-unit stop or a
// serialised source. Its borrowed OP stays held through every owned join.
type noneStopCompletionV1 struct {
	mu                                sync.Mutex
	scope                             *nativeStopScopeV1
	join                              *none.JoinOwnerV1
	read, write                       *os.File
	committed, borrowClosed, finished bool
	failure                           error
}

func (s *Supervisor) nativeNoneStopRequestV1(client *client, scope *nativeStopScopeV1, request proto.Request) bool {
	if request.Op != proto.NativeNonePrepareV1 && request.Op != proto.NativeNoneCommitV1 {
		return false
	}
	var args proto.NativeStopArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != scope.state.Primary || args.Nonce != scope.state.Nonce || !s.unwalledV1() || scope.none == nil {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the unwalled stop has another original native scope")))
		return true
	}
	if err := scope.line.RejectAncillary(); err != nil {
		client.close()
		return true
	}
	if err := scope.checkCurrentV1(scope.lifetime); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	if request.Op == proto.NativeNonePrepareV1 {
		if request.ID != proto.NativeNonePrepareRequestV1 {
			client.reply(request.ID, nil, toProtoErr(execRefusal("the unwalled completion preparation has another request")))
			return true
		}
		scope.mu.Lock()
		exists := scope.noneStop != nil || scope.used || scope.warmDone != nil || scope.warmMutation
		scope.mu.Unlock()
		if exists {
			client.reply(request.ID, nil, toProtoErr(execRefusal("this original native scope already has an unwalled completion")))
			return true
		}
		join, err := scope.none.PrepareJoinV1(scope.lifetime)
		if err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		read, write, err := os.Pipe()
		if err != nil {
			join.Close()
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		proof := &noneStopCompletionV1{scope: scope, join: join, read: read, write: write}
		scope.mu.Lock()
		scope.noneStop = proof
		scope.used = true
		scope.mu.Unlock()
		root := scope.root
		root.mu.Lock()
		if root.noneStops == nil {
			root.noneStops = map[*noneStopCompletionV1]struct{}{}
		}
		root.noneStops[proof] = struct{}{}
		root.mu.Unlock()
		if err := client.replyNoneStopPipeV1(request.ID, scope.state, read); err != nil {
			client.close()
		}
		return true
	}
	if request.ID != proto.NativeNoneCommitRequestV1 {
		client.close()
		return true
	}
	scope.mu.Lock()
	proof := scope.noneStop
	scope.mu.Unlock()
	if proof == nil {
		client.close()
		return true
	}
	proof.mu.Lock()
	if proof.committed || proof.finished {
		proof.mu.Unlock()
		client.close()
		return true
	}
	proof.committed = true
	proof.mu.Unlock()
	// Commit is one-way. The sole acknowledgement is the completion pipe,
	// after actual physical joins and closure of this SAME incoming OP loan.
	scope.root.markStopIntent()
	select {
	case scope.root.stop <- struct{}{}:
	default:
	}
	return true
}

func (client *client) replyNoneStopPipeV1(id uint64, state proto.NativeStopStateV1, read *os.File) error {
	connection, ok := client.conn.(*net.UnixConn)
	if !ok {
		return execRefusal("the unwalled completion has no original Unix connection")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(proto.Response{ID: id, OK: true, Result: raw})
	if err != nil {
		return err
	}
	client.wmu.Lock()
	defer client.wmu.Unlock()
	if client.writeErr != nil {
		return client.writeErr
	}
	_ = connection.SetWriteDeadline(time.Now().Add(clientWriteWait))
	err = proto.SendUnixFrameV1(connection, proto.TResponse, payload, []*os.File{read})
	_ = connection.SetWriteDeadline(time.Time{})
	if err != nil {
		client.writeErr = err
	}
	return err
}

func (proof *noneStopCompletionV1) closeBorrowV1() error {
	proof.mu.Lock()
	defer proof.mu.Unlock()
	if proof.borrowClosed {
		return nil
	}
	committed := proof.committed
	if committed {
		<-proof.scope.cleaned
		root := proof.scope.root
		root.mu.Lock()
		failure := root.runtimeCleanupErr
		root.mu.Unlock()
		if err := errors.Join(failure, proof.join.CheckJoinedV1(context.Background())); err != nil {
			return err
		}
	}
	scope := proof.scope
	scope.mu.Lock()
	operation := scope.operation
	scope.mu.Unlock()
	if operation == nil {
		return execRefusal("the unwalled completion lost its original incoming OP")
	}
	if err := operation.CloseWithError(); err != nil {
		return err
	}
	scope.mu.Lock()
	if scope.operation == operation {
		scope.operation = nil
	}
	scope.released = true
	scope.mu.Unlock()
	proof.borrowClosed = true
	if !committed {
		proof.finished = true
		_ = proof.read.Close()
		_ = proof.write.Close()
		_ = proof.join.Close()
		scope.root.mu.Lock()
		delete(scope.root.noneStops, proof)
		scope.root.mu.Unlock()
	}
	return nil
}

func (s *Supervisor) finishNoneStopsV1(returned error) {
	s.mu.Lock()
	proofs := make([]*noneStopCompletionV1, 0, len(s.noneStops))
	for proof := range s.noneStops {
		proofs = append(proofs, proof)
	}
	s.mu.Unlock()
	for _, proof := range proofs {
		proof.mu.Lock()
		committed := proof.committed
		finished := proof.finished
		proof.mu.Unlock()
		if finished {
			continue
		}
		if !committed {
			_ = proof.read.Close()
			_ = proof.write.Close()
			_ = proof.join.Close()
			continue
		}
		failure := errors.Join(returned, proof.closeBorrowV1())
		if failure != nil {
			_, _ = proof.write.Write([]byte{0})
			_ = proof.write.Close()
			s.log.Error("the original unwalled stop remains retained", "err", failure)
			// Preserve the original process, native owner and SAME OP. Neither
			// timeout nor logical EOF releases an uncertain physical lifetime.
			select {}
		}
		if _, err := proof.write.Write([]byte{1}); err != nil {
			// Physical and incoming-OP joins are complete. A vanished
			// acknowledgement reader adds no new physical lifetime.
			s.log.Warn("the original unwalled completion reader closed", "err", err)
		}
		_ = proof.read.Close()
		_ = proof.write.Close()
		_ = proof.join.Close()
		proof.mu.Lock()
		proof.finished = true
		proof.mu.Unlock()
	}
	if runtime, ok := s.sb.(noneRuntime); ok && s.sandboxStartAttempted && !runtime.cleanupConfirmedV1() && !runtime.preparedCleanupConfirmedV1() {
		s.log.Error("retaining the original unwalled owner after uncertain physical cleanup")
		select {}
	}
}

func (s *Supervisor) retainUnconfirmedNonePublicationV1() {
	if s.noneCleanupCommitErr == nil {
		return
	}
	s.log.Error("retaining the original unwalled holder after an unconfirmed cleanup publication", "err", s.noneCleanupCommitErr)
	select {}
}

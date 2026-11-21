//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/version"
)

// RunAuxiliaryGuestV1 is one closed native helper command. Direct invocation
// cannot supply an OP, select a unit/runtime or self-authorise a saved plan.
func RunAuxiliaryGuestV1(primary string) (returned error) {
	if !validID(primary) {
		return auxiliaryUnavailableV1("the auxiliary helper needs one exact original primary")
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	lifetime, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	manager, err := openNativeUserManagerV1(lifetime)
	if err != nil {
		return err
	}
	if err := checkAuxiliarySelfMembershipV1(lifetime, manager, primary, 0); err != nil {
		return errors.Join(err, manager.Close())
	}
	if err := manager.Close(); err != nil {
		return err
	}
	if err := InitPlacementContext(lifetime); err != nil {
		return err
	}
	socket := paths.Socket(primary)
	for _, directory := range []string{filepath.Dir(filepath.Dir(socket)), filepath.Dir(socket)} {
		if err := paths.EnsurePrivateDir(directory); err != nil {
			return err
		}
	}
	listener, err := listenPrivate(socket, "auxiliary original-operation socket")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, listener.Close()); _ = os.Remove(socket) }()
	// This listener accepts exactly the original outside creator's one loan.
	// Reconnection cannot revive the source or create another native guest.
	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(time.Now().Add(20 * time.Second))
	}
	connection, err := listener.Accept()
	if err != nil {
		return err
	}
	candidate, err := PrepareAuxiliaryBootCandidateV1(lifetime, primary, connection)
	if err != nil {
		return errors.Join(err, connection.Close())
	}
	defer func() { returned = errors.Join(returned, candidate.Close()) }()
	_ = candidate.line.SetDeadline(time.Now().Add(20 * time.Second))
	frame, err := candidate.ReadFrame()
	if err != nil {
		return err
	}
	if err := candidate.line.AcceptNativeHelloV1(frame); err != nil {
		return err
	}
	var hello proto.Hello
	if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != NativeStartupRoleV1 {
		return auxiliaryUnavailableV1("the original auxiliary connection has another hello")
	}
	if err := candidate.line.bindOriginalNativePeerV1(candidate.peer); err != nil {
		return err
	}
	if err := candidate.peer.Check(); err != nil {
		return err
	}
	if err := candidate.line.WriteNativeHelloV1(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "operation-supervisor-v1", StartupNonce: candidate.Challenge()}); err != nil {
		return err
	}
	frame, err = candidate.ReadFrame()
	if err != nil {
		return err
	}
	boot, err := candidate.Accept(lifetime, frame)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, boot.Close()) }()
	frame, err = candidate.ReadFrame()
	if err != nil {
		return err
	}
	if err := candidate.line.RejectAncillary(); err != nil {
		return err
	}
	var request proto.Request
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 2 || request.Op != auxiliaryReadyOperationV1 || len(request.Args) != 0 {
		return auxiliaryUnavailableV1("the auxiliary boot has no exact original ready request")
	}
	sender, err := candidate.line.currentSender()
	if err != nil {
		return err
	}
	if err := candidate.peer.CheckSender(sender); err != nil {
		return err
	}
	// Boot uses the original outside lifetime; readiness is only correlation.
	inner, err := boot.startAuxiliaryGuestV1(boot.Context())
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, inner.Close()) }()
	if err := candidate.WriteFrame(proto.TResponse, proto.Response{ID: 2, OK: true, Result: json.RawMessage(`{"operation_nonce":"` + candidate.nonce + `"}`)}); err != nil {
		return err
	}
	if err := candidate.line.SetDeadline(time.Time{}); err != nil {
		return err
	}
	outer := link.New(auxiliaryLineConnectionV1{Conn: connection, line: candidate.line}, link.Guest, link.Options{Opener: auxiliaryOpenersV1})
	defer func() { returned = errors.Join(returned, outer.Close()) }()
	return serveAuxiliaryReadOnlyV1(boot.Context(), outer, inner)
}
func (candidate *AuxiliaryBootCandidateV1) Close() error {
	if candidate == nil {
		return nil
	}
	return errors.Join(candidate.line.Close(), candidate.peer.Close(), candidate.manager.Close())
}

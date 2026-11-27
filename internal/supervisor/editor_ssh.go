package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// This purpose-specific owner is minted only in ssh.open's actual accepted
// full client handler. It retains the native objects of that current run.
// A native-stop DTO, Session row or serialised correlation cannot issue it.
type editorSSHScopeV1 struct {
	peer            *client
	root, selected  *Supervisor
	owner           *host.OwnedSupervisorLease
	runtime         sandbox
	cleaned         <-chan struct{}
	lifetime        context.Context
	native          *host.OriginalVMNeed
	connection      net.Conn
	process         *process.CopyOwnerV1
	gvisor          *gvisor.LiveOwnerV1
	guest           *link.Session
	generation, run string
	ctx             context.Context
	cancel          context.CancelFunc
	workers         sync.WaitGroup
}

func init() { Features = append(Features, proto.FeatureEditorSSHV1) }

func (s *Supervisor) editorSSHViewRequestV1(c *client, request proto.Request) bool {
	if request.Op != "ssh.view" {
		return false
	}
	var args struct{}
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil {
		c.reply(request.ID, nil, editorSSHRefusalV1("invalid SSH display request"))
		return true
	}
	s.mu.Lock()
	open := s.editorSSHCount > 0
	s.mu.Unlock()
	c.reply(request.ID, proto.EditorSSHViewV1{Version: 1, Open: open}, nil)
	return true
}

func editorSSHRefusalV1(message string) *proto.Error {
	return &proto.Error{Code: "ssh_refused", Message: message, Hint: "Check the session through the client, then reconnect with SSH."}
}

func (s *Supervisor) editorSSHFrameV1(c *client, reader *bufio.Reader, frame proto.Frame, first bool) bool {
	if frame.Type != proto.TRequest {
		return false
	}
	var request proto.Request
	if json.Unmarshal(frame.Payload, &request) != nil || request.Op != proto.EditorSSHOpenV1 {
		return false
	}
	if !first || !c.full {
		c.reply(request.ID, nil, editorSSHRefusalV1("SSH requires an original full dedicated connection"))
		return true
	}
	s.serveEditorSSHV1(c, reader, request)
	return true
}

func (s *Supervisor) serveEditorSSHV1(c *client, reader *bufio.Reader, request proto.Request) {
	var args proto.EditorSSHOpenArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) {
		c.reply(request.ID, nil, editorSSHRefusalV1("invalid SSH session selection"))
		return
	}
	root := s.owner()
	selected := root.member(args.Session)
	if selected == nil || selected != s {
		c.reply(request.ID, nil, editorSSHRefusalV1("the dedicated peer selected another session"))
		return
	}
	root.mu.Lock()
	scope := &editorSSHScopeV1{peer: c, root: root, selected: selected, owner: root.bootOwner, runtime: root.sb, cleaned: root.runtimeCleaned, lifetime: root.lifetimeContext(), native: host.OriginalVMNeedFromContext(root.lifetimeContext()), connection: root.vmStartConnection, process: root.processCopy, gvisor: root.gvisorLive, guest: root.link, generation: root.generation}
	root.mu.Unlock()
	selected.mu.Lock()
	scope.run = selected.runToken
	selected.mu.Unlock()
	scope.ctx, scope.cancel = context.WithCancel(c.requestContextV1(scope.lifetime))
	defer scope.cancel()
	if err := scope.check(scope.ctx); err != nil {
		c.reply(request.ID, nil, toProtoErr(err))
		return
	}
	// Count the actual retained SSH owner, in memory only. It remains live
	// until this carrier and every real guest stream/child have joined.
	selected.mu.Lock()
	selected.editorSSHCount++
	selected.mu.Unlock()
	defer func() { selected.mu.Lock(); selected.editorSSHCount--; selected.mu.Unlock() }()
	root.audit.write("ssh_open", map[string]any{"session": selected.id})
	c.reply(request.ID, proto.EditorSSHOpenResultV1{Version: 1, Session: selected.id, Root: root.id}, nil)
	carrier := link.ContinueEditorSSH(c.conn, reader, link.Supervisor)
	stop := context.AfterFunc(scope.ctx, func() { _ = carrier.Close() })
	defer func() { scope.cancel(); _ = carrier.Close(); stop(); scope.workers.Wait() }()
	for {
		stream, err := carrier.Accept(scope.ctx)
		if err != nil {
			return
		}
		scope.workers.Add(1)
		go func() { defer scope.workers.Done(); defer stream.Close(); scope.serveChannel(stream) }()
	}
}

func (scope *editorSSHScopeV1) check(ctx context.Context) error {
	if scope == nil || ctx == nil || scope.peer == nil || !scope.peer.full || scope.root == nil || scope.selected == nil || scope.owner == nil || scope.lifetime == nil || scope.run == "" {
		return editorSSHRefusalV1("the original native SSH owner is absent")
	}
	if err := errors.Join(ctx.Err(), scope.lifetime.Err()); err != nil {
		return err
	}
	root := scope.root
	root.mu.Lock()
	live := root.bootOwner == scope.owner && root.generation == scope.generation && root.runtimeCleaned == scope.cleaned && scope.cleaned != nil && !channelClosed(scope.cleaned) && root.sb == scope.runtime && root.launched && root.rec.phase == host.LifeRunning && root.link == scope.guest && scope.guest != nil && scope.guest.Err() == nil
	lifetime := root.lifetimeContext()
	same := lifetime == scope.lifetime && host.OriginalVMNeedFromContext(lifetime) == scope.native && root.vmStartConnection == scope.connection && root.processCopy == scope.process && root.gvisorLive == scope.gvisor
	root.mu.Unlock()
	scope.selected.mu.Lock()
	memberLive := scope.selected.runToken == scope.run && scope.selected.launched
	scope.selected.mu.Unlock()
	if !live || !same || !memberLive || root.member(scope.selected.id) != scope.selected || !scope.owner.Valid(root.id) || !scope.owner.StartupCommittedV1() {
		return editorSSHRefusalV1("the selected original native SSH controller ended or changed")
	}
	select {
	case <-scope.peer.done:
		return editorSSHRefusalV1("the original accepted SSH peer closed")
	default:
	}
	switch scope.runtime.(type) {
	case vmRuntime:
		if scope.native == nil {
			return editorSSHRefusalV1("the SSH VM has no original native receipt")
		}
		return errors.Join(scope.native.CheckRunningNativeV1(ctx, scope.connection), ctx.Err())
	case processSandbox:
		if scope.gvisor != nil {
			return scope.gvisor.Check(ctx)
		}
		if scope.process == nil {
			return editorSSHRefusalV1("the SSH process has no original live native continuation")
		}
		return scope.process.CheckLiveV1(ctx)
	default:
		return editorSSHRefusalV1("the selected SSH runtime has no original supported native continuation")
	}
}

// The same actual writer guard retains native member mutation serialisation.
// Source checks happen outside link.mu, and cancellation can stop admission.
func (scope *editorSSHScopeV1) openTurn(ctx context.Context) (func(), error) {
	for !scope.root.spawnMu.TryLock() {
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-scope.ctx.Done():
			timer.Stop()
			return nil, scope.ctx.Err()
		case <-timer.C:
		}
	}
	if err := scope.check(ctx); err != nil {
		scope.root.spawnMu.Unlock()
		return nil, err
	}
	return scope.root.spawnMu.Unlock, nil
}

func (scope *editorSSHScopeV1) serveChannel(clientStream *link.Stream) {
	var header proto.EditorChannelV1
	if !api.ExactJSONFields(clientStream.HeaderBytes(), struct {
		Kind string `json:"kind"`
		proto.EditorChannelV1
	}{}) || clientStream.Header(&header) != nil || header.Version != 1 || scope.check(scope.ctx) != nil {
		return
	}
	switch clientStream.Kind() {
	case link.KindCtl:
		if header.Command != "" || header.Port != 0 || header.Term != "" || header.Cols != 0 || header.Rows != 0 || len(header.Modes) != 0 {
			return
		}
		writer := proto.NewWriter(clientStream)
		for {
			frame, err := link.ReadEditorFrameV1(clientStream, 16)
			if err != nil || frame.Type != proto.TPing || len(frame.Payload) != 0 || scope.check(scope.ctx) != nil {
				return
			}
			if writer.Write(proto.TPong, nil) != nil {
				return
			}
		}
	case link.KindExec:
		if header.Command == "" || len(header.Command) > proto.EditorSSHCommandMaxV1 || header.Port != 0 || header.Term != "" || header.Cols != 0 || header.Rows != 0 || len(header.Modes) != 0 {
			return
		}
		scope.exec(clientStream, header.Command)
	case link.KindHolder:
		if header.Port != 0 || len(header.Command) > proto.EditorSSHCommandMaxV1 || header.Term == "" || len(header.Term) > 128 || header.Cols == 0 || header.Rows == 0 || header.Cols > 1000 || header.Rows > 1000 || len(header.Modes) > 4096 {
			return
		}
		scope.guestCommand(clientStream, header, proto.EditorTerminalV1)
	case link.KindSFTP:
		if header.Command != "" || header.Port != 0 || header.Term != "" || header.Cols != 0 || header.Rows != 0 || len(header.Modes) != 0 {
			return
		}
		scope.guestCommand(clientStream, header, proto.EditorSFTPV1)
	case link.KindPort:
		if header.Port < 1 || header.Port > 65535 || header.Command != "" || header.Term != "" || header.Cols != 0 || header.Rows != 0 || len(header.Modes) != 0 {
			return
		}
		scope.port(clientStream, int(header.Port))
	}
}

func (scope *editorSSHScopeV1) exec(output *link.Stream, command string) {
	id := host.NewID()
	ctx, cancel := context.WithTimeout(scope.ctx, bootTimeout)
	err := scope.root.ctl.request(ctx, proto.CtlEditorPrepareV1, proto.CtlEditorPrepareArgsV1{Version: 1, ID: id, Session: scope.selected.id, Run: scope.run, Purpose: proto.EditorExecV1, Command: command}, nil)
	cancel()
	if err != nil {
		return
	}
	defer scope.cancelGuest(id)
	guest, err := scope.guest.OpenGuarded(scope.ctx, link.KindExec, proto.ExecHeader{ID: id, Purpose: proto.EditorExecV1}, scope.openTurn)
	if err != nil {
		return
	}
	defer guest.Close()
	_ = link.RelayDuplex(scope.ctx, output, guest)
}

func (scope *editorSSHScopeV1) guestCommand(output *link.Stream, header proto.EditorChannelV1, purpose string) {
	id := host.NewID()
	ctx, cancel := context.WithTimeout(scope.ctx, bootTimeout)
	err := scope.root.ctl.request(ctx, proto.CtlEditorPrepareV1, proto.CtlEditorPrepareArgsV1{Version: 1, ID: id, Session: scope.selected.id, Run: scope.run, Purpose: purpose, Command: header.Command}, nil)
	cancel()
	if err != nil {
		return
	}
	defer scope.cancelGuest(id)
	guest, err := scope.guest.OpenGuarded(scope.ctx, output.Kind(), proto.EditorGuestHeaderV1{Version: 1, ID: id, Purpose: purpose}, scope.openTurn)
	if err != nil {
		return
	}
	defer guest.Close()
	if purpose == proto.EditorTerminalV1 {
		if err := proto.NewWriter(guest).WriteJSON(proto.TRequest, proto.EditorTerminalConfigV1{Term: header.Term, Cols: header.Cols, Rows: header.Rows, Modes: header.Modes}); err != nil {
			return
		}
	}
	_ = link.RelayDuplex(scope.ctx, output, guest)
}

func (scope *editorSSHScopeV1) port(output *link.Stream, port int) {
	root := scope.root
	root.portMu.Lock()
	root.mu.Lock()
	allowed, _, err := host.PortPolicyGrant(root.m.Policy, port, time.Now())
	root.mu.Unlock()
	grant := root.portListeners[port]
	valid := err == nil && allowed && !root.portsClosed && grant != nil && grant.ctx.Err() == nil
	if valid {
		select {
		case grant.limit <- struct{}{}:
			grant.wg.Add(1)
		default:
			valid = false
		}
	}
	root.portMu.Unlock()
	if !valid {
		_ = proto.NewWriter(output).WriteJSON(proto.TResponse, proto.EditorReadyV1{Version: 1, Error: &proto.Error{Code: "port_refused", Message: "the guest loopback port has no existing live grant", Hint: brand.Name + " policy allow port " + scope.selected.id + " (grant the exact port, then reconnect)"}})
		return
	}
	defer grant.wg.Done()
	defer func() { <-grant.limit }()
	flow, cancel := context.WithCancel(scope.ctx)
	stop := context.AfterFunc(grant.ctx, cancel)
	defer func() { cancel(); stop() }()
	guest, err := scope.guest.OpenGuarded(flow, link.KindPort, proto.PortHeaderV1{Version: 1, Port: port}, func(ctx context.Context) (func(), error) {
		release, err := scope.openTurn(ctx)
		if err != nil {
			return nil, err
		}
		grantRelease, err := root.portGrantTurnV1(ctx, grant)
		if err != nil {
			release()
			return nil, err
		}
		return func() { grantRelease(); release() }, nil
	})
	if err != nil {
		return
	}
	defer guest.Close()
	if proto.NewWriter(output).WriteJSON(proto.TResponse, proto.EditorReadyV1{Version: 1, Ready: true}) != nil {
		return
	}
	root.audit.write("port", map[string]any{"port": port, "source": "ssh", "session": scope.selected.id})
	_ = link.RelayDuplex(flow, output, guest)
}

// The actual existing grant owns this per-grant writer gate. Revokers cancel
// its context first, drop portMu, then join the gate. The guarded writer owns
// raw-I/O interruption, so revocation never waits behind an uninterruptible
// syscall while preventing the later guest-link close that would release it.
func (root *Supervisor) portGrantTurnV1(ctx context.Context, grant *portForward) (func(), error) {
	if grant == nil {
		return nil, portFailure("the original exact port grant is absent")
	}
	for !grant.openMu.TryLock() {
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-grant.ctx.Done():
			timer.Stop()
			return nil, grant.ctx.Err()
		case <-timer.C:
		}
	}
	root.portMu.Lock()
	root.mu.Lock()
	allowed, _, err := host.PortPolicyGrant(root.m.Policy, grant.port, time.Now())
	root.mu.Unlock()
	valid := ctx.Err() == nil && !root.portsClosed && root.portListeners[grant.port] == grant && grant.ctx.Err() == nil && allowed && err == nil
	root.portMu.Unlock()
	if !valid {
		grant.openMu.Unlock()
		return nil, portFailure("the original exact port grant ended")
	}
	return grant.openMu.Unlock, nil
}

func (scope *editorSSHScopeV1) cancelGuest(id string) {
	cleanup, end := context.WithTimeout(context.Background(), clientWriteWait)
	defer end()
	if err := scope.root.ctl.request(cleanup, proto.CtlEditorCancelV1, proto.CtlEditorCancelArgsV1{Version: 1, ID: id}, nil); err != nil {
		// A failed cleanup acknowledgement cannot free the live SSH count
		// while an actual guest child may remain. End this owned controller
		// and join its real native cleanup before releasing the scope.
		scope.root.failMember()
		if scope.root.cancelLifetime != nil {
			scope.root.cancelLifetime(editorSSHRefusalV1("the original guest editor cleanup acknowledgement was lost"))
		}
		select {
		case scope.root.stop <- struct{}{}:
		default:
		}
		if scope.cleaned != nil {
			<-scope.cleaned
		}
	}
}

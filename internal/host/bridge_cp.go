package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type bridgeCopyRoute struct {
	session string
	line    *holderLine
	stop    func() bool
	once    sync.Once
}

func init() { Features = append(Features, proto.FeatureFileTransferV1, "cp") }

func (r *bridgeCopyRoute) close() {
	if r != nil {
		r.once.Do(func() {
			if r.stop != nil {
				r.stop()
			}
			_ = r.line.c.Close()
		})
	}
}

func (b *bridge) closeCopy() {
	b.copyMu.Lock()
	b.copyClosed = true
	route := b.copyRoute
	b.copyMu.Unlock()
	route.close()
}

func dialCopyHolder(ctx context.Context, id string) (_ *holderLine, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket(id))
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, conn.Close())
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	line := &holderLine{c: conn, w: proto.NewWriter(conn), r: bufio.NewReader(conn)}
	if err := line.w.WriteJSONCtx(ctx, proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		return nil, err
	}
	frame, err := proto.Read(line.r)
	if err != nil || frame.Type != proto.THello {
		return nil, UserError("helper_old", "the running sandbox did not answer its copy handshake", "Stop and resume the sandbox with the current helper.")
	}
	var hello proto.Hello
	if json.Unmarshal(frame.Payload, &hello) != nil {
		return nil, UserError("helper_old", "the running sandbox returned an invalid copy handshake", "Stop and resume the sandbox with the current helper.")
	}
	if err := requireSupervisorFeatures(hello, proto.FeatureFileTransferV1); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	complete = true
	return line, nil
}

func (b *bridge) copyConnection(ctx context.Context, id string) (*bridgeCopyRoute, error) {
	b.copyMu.Lock()
	closed, route := b.copyClosed, b.copyRoute
	b.copyMu.Unlock()
	if closed {
		return nil, UserError("cp_closed", "the copy connection has ended", "Start the copy again on a fresh connection.")
	}
	if route != nil {
		if route.session != id {
			return nil, UserError("cp_path", "this copy connection already selects another sandbox", "Use one sandbox per copy command.")
		}
		return route, nil
	}
	manifest, err := LoadManifest(id)
	if err != nil {
		return nil, err
	}
	if manifest.Wall == nil || manifest.Wall.Level == "none" {
		return nil, UserError("cp_unwalled", "the selected session runs without a wall", "Use scp for that host's own files.")
	}
	if manifest.Wall.Level != "process" && manifest.Wall.Level != "gvisor" && manifest.Wall.Level != "microvm" {
		return nil, UserError("cp_path", "the recorded sandbox wall is unknown", "Resume the sandbox with the matching helper.")
	}
	if !alive(id) {
		return nil, UserError("sandbox_stopped", "the sandbox is stopped", "Resume the sandbox, then copy its files.")
	}
	line, err := dialCopyHolder(ctx, id)
	if err != nil {
		return nil, err
	}
	route = &bridgeCopyRoute{session: id, line: line}
	route.stop = context.AfterFunc(ctx, func() { _ = line.c.Close() })
	b.copyMu.Lock()
	if b.copyClosed || ctx.Err() != nil {
		b.copyMu.Unlock()
		route.close()
		return nil, UserError("cp_closed", "the copy connection ended before admission", "Start the copy again.")
	}
	b.copyRoute = route
	startWatch := !b.copyWatching
	b.copyWatching = true
	b.copyMu.Unlock()
	if startWatch && b.ownedOutput != nil {
		go func() { _ = b.ownedOutput.Wait(); b.closeCopy() }()
	}
	return route, nil
}

func (b *bridge) handleCopy(req proto.Request) bool {
	if req.Op != "cp.list" && req.Op != "cp.read" {
		return false
	}
	var id string
	if req.Op == "cp.list" {
		var args proto.CopyListArgsV1
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.Version != proto.FileTransferVersionV1 {
			b.reply(req.ID, nil, UserError("cp_path", "invalid copy list declaration", "Use the matching client and helper."))
			return true
		}
		id = args.ID
	} else {
		var args proto.CopyReadArgsV1
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.Version != proto.FileTransferVersionV1 {
			b.reply(req.ID, nil, UserError("cp_path", "invalid copy read declaration", "Use the matching client and helper."))
			return true
		}
		id = args.ID
	}
	if !api.ValidSessionID(id) {
		b.reply(req.ID, nil, UserError("cp_path", "the copy has no exact selected session", "List sessions and use the full address."))
		return true
	}
	b.copyMu.Lock()
	if b.copyBusy {
		b.copyMu.Unlock()
		b.reply(req.ID, nil, UserError("cp_busy", "the preceding copy request has not joined", "Wait for it before sending another copy request."))
		return true
	}
	b.copyBusy = true
	b.copyMu.Unlock()
	defer func() { b.copyMu.Lock(); b.copyBusy = false; b.copyMu.Unlock() }()
	route, err := b.copyConnection(b.ctx, id)
	if err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	if err := route.line.w.WriteJSONCtx(b.ctx, proto.TRequest, req); err != nil {
		route.close()
		b.reply(req.ID, nil, err)
		return true
	}
	for {
		frame, err := proto.Read(route.line.r)
		if err != nil {
			route.close()
			b.reply(req.ID, nil, err)
			return true
		}
		if frame.Type != proto.TResponse {
			continue
		}
		var response proto.Response
		if json.Unmarshal(frame.Payload, &response) != nil || response.ID != req.ID {
			route.close()
			b.reply(req.ID, nil, UserError("cp_path", "the copy response changed its request identity", "Run the copy again with the current helper."))
			return true
		}
		if !response.OK {
			if response.Error != nil {
				b.reply(req.ID, nil, response.Error)
			} else {
				b.reply(req.ID, nil, UserError("cp_path", "the sandbox refused the copy", "Check the selected path, then try again."))
			}
			return true
		}
		b.reply(req.ID, json.RawMessage(response.Result), nil)
		return true
	}
}

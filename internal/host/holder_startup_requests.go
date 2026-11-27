package host

import (
	"bufio"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func (p *holderStartup) startupFrame(c *hclient, r *bufio.Reader, frame proto.Frame) startupDispatchResult {
	// A hook may not apply events before the actual Holder exists.
	if c.access == accessHook && frame.Type == proto.TRequest {
		var req proto.Request
		if json.Unmarshal(frame.Payload, &req) != nil {
			return startupClose
		}
		if req.Op == "hook" {
			p.reply(c, req.ID, nil, pendingHolderError())
			return startupContinue
		}
	}
	if c.access != accessFull {
		if !p.peer.serveLimited(c, frame) {
			return startupClose
		}
		return startupContinue
	}
	if frame.Type == proto.TPing {
		_ = c.w.Write(proto.TPong, frame.Payload)
		return startupContinue
	}
	if frame.Type != proto.TRequest {
		_ = c.w.WriteJSON(proto.TError, pendingHolderError())
		return startupClose
	}
	var req proto.Request
	if json.Unmarshal(frame.Payload, &req) != nil {
		return startupClose
	}
	return p.startupRequest(c, r, req)
}

func (p *holderStartup) startupRequest(c *hclient, r *bufio.Reader, req proto.Request) startupDispatchResult {
	p.mu.Lock()
	if p.active != nil {
		active := p.active
		p.mu.Unlock()
		prepareStartupDispatch(c)
		active.request(c, req)
		active.dispatch(c, r)
		return startupClose
	}
	switch req.Op {
	case "rename":
		p.mu.Unlock()
		p.renameRequest(c, req)
		return startupContinue
	case "kill", cancelPendingStartOp:
		return p.stopRequestLocked(c, req)
	case "sockets":
		p.socketsRequestLocked(c, req)
	case "secrets":
		p.secretsRequestLocked(c, req)
	default:
		p.mu.Unlock()
		p.reply(c, req.ID, nil, pendingHolderError())
	}
	return startupContinue
}

// Each Locked request helper consumes the same gate and always unlocks it
// before writing a response. They never acquire another startup owner.
func (p *holderStartup) stopRequestLocked(c *hclient, req proto.Request) startupDispatchResult {
	// After PTY entry, only the ordinary running owner may stop the child.
	// This pending owner grants no process or descendant authority.
	if p.launching || p.closed {
		p.mu.Unlock()
		p.reply(c, req.ID, nil, pendingHolderError())
		return startupClose
	}
	p.cancel()
	p.waiting, p.want = false, nil
	p.stops = append(p.stops, c)
	p.stopIDs = append(p.stopIDs, req.ID)
	delete(p.connections, c.conn)
	p.mu.Unlock()
	return startupRetain
}

func (p *holderStartup) socketsRequestLocked(c *hclient, req proto.Request) {
	ready := p.ready && p.ctx.Err() == nil
	p.mu.Unlock()
	if ready {
		p.reply(c, req.ID, socketsState{PeerCheck: p.peer.peerCheck, Version: version.Version}, nil)
	} else {
		p.reply(c, req.ID, nil, pendingHolderError())
	}
}

func (p *holderStartup) secretsRequestLocked(c *hclient, req proto.Request) {
	if !p.waiting || p.ctx.Err() != nil || !time.Now().Before(p.secretDeadline) {
		p.waiting, p.want = false, nil
		p.mu.Unlock()
		p.reply(c, req.ID, nil, pendingHolderError())
		return
	}
	env, err := secretEnvironment(req.Args, p.want)
	if err != nil {
		p.mu.Unlock()
		p.reply(c, req.ID, nil, &proto.Error{Code: "bad_request", Message: err.Error(), Hint: "Send the required secrets again."})
		return
	}
	if !time.Now().Before(p.secretDeadline) {
		p.waiting, p.want = false, nil
		p.mu.Unlock()
		p.reply(c, req.ID, nil, pendingHolderError())
		return
	}
	p.waiting, p.want = false, nil
	p.secretResult = env
	close(p.secretReady)
	p.mu.Unlock()
	p.reply(c, req.ID, map[string]bool{"ok": true}, nil)
}

package host

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (h *Holder) request(c *hclient, req proto.Request) {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = c.w.WriteJSON(proto.TResponse, resp)
	}
	switch req.Op {
	case "info":
		h.mu.Lock()
		m := *h.m
		h.mu.Unlock()
		reply(m, nil)
	case "peek":
		var args struct {
			Bytes int `json:"bytes"`
		}
		_ = json.Unmarshal(req.Args, &args)
		if args.Bytes <= 0 || args.Bytes > 1<<20 {
			args.Bytes = 256 << 10
		}
		data, start, cols, rows := h.peek(args.Bytes)
		reply(map[string]any{"data": data, "start": start, "cols": cols, "rows": rows}, nil)
	case "rename":
		var args struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Args, &args) != nil || strings.TrimSpace(args.Name) == "" {
			reply(nil, &proto.Error{Code: "bad_request", Message: "a name is needed"})
			return
		}
		h.mu.Lock()
		h.m.Name = strings.TrimSpace(args.Name)
		h.dirty = true
		h.mu.Unlock()
		reply(map[string]string{"name": args.Name}, nil)
	case "ports":
		h.mu.Lock()
		unit, pid := h.m.Run.Unit != "", h.m.Run.AgentPID
		h.mu.Unlock()
		reply(listeningPorts(sessionPIDs(unit, pid)), nil)
	case "hook":
		var ev HookEvent
		if json.Unmarshal(req.Args, &ev) == nil {
			h.mu.Lock()
			h.applyHook(ev)
			h.mu.Unlock()
		}
		reply(map[string]bool{"ok": true}, nil)
	case "send":
		var args struct {
			Text  string `json:"text"`
			Enter bool   `json:"enter"`
		}
		if json.Unmarshal(req.Args, &args) != nil {
			reply(nil, &proto.Error{Code: "bad_request", Message: "text is needed"})
			return
		}
		h.mu.Lock()
		exited := h.exited
		h.needInput = false
		d := h.al.d
		h.mu.Unlock()
		if exited {
			reply(nil, &proto.Error{Code: "exited", Message: "the session has ended"})
			return
		}
		data := []byte(args.Text)
		if args.Enter {
			data = append(data, '\r')
		}
		noteTyping(d, data)
		if _, err := h.ptmx.Write(data); err != nil {
			reply(nil, &proto.Error{Code: "write", Message: err.Error()})
			return
		}
		reply(map[string]int{"bytes": len(data)}, nil)
	case "kill":
		reply(map[string]bool{"killed": true}, nil)
		go h.stop("killed by request", false)
	default:
		if v, perr, ok := h.debugRequest(req); ok {
			reply(v, perr)
			return
		}
		reply(nil, &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op})
	}
}

// peek returns what draws the session's screen in a blank terminal of cols
// by rows: the shadow terminal's screen and the output after it, as an
// attach gets, or without it, the newest n bytes of output. A tail alone
// can start after the screen was drawn and miss most of it (#85). start is
// the stream offset of the output in data.
func (h *Holder) peek(n int) (data []byte, start int64, cols, rows int) {
	h.mu.Lock()
	vt := h.vt
	h.mu.Unlock()
	var snap []byte
	snapAt := int64(-1)
	if vt != nil {
		// Well within the bridge's 3 s, so a slow one still leaves time for the tail.
		if s, upTo, err := vt.Screen(time.Second); err == nil {
			snap, snapAt = s, upTo
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cols, rows = h.m.Term.Cols, h.m.Term.Rows
	if snap != nil {
		if rest, ok := h.ring.ReadFrom(snapAt); ok {
			return append(snap, rest...), snapAt, cols, rows
		}
	}
	data, start = h.ring.Tail(n)
	return data, start, cols, rows
}

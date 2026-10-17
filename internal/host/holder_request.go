package host

import (
	"encoding/json"
	"strings"

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
		h.mu.Lock()
		data, start := h.ring.Tail(args.Bytes)
		cols, rows := h.m.Term.Cols, h.m.Term.Rows
		h.mu.Unlock()
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

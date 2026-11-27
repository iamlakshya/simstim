package host

import (
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (b *bridge) reply(id uint64, v any, err error) (proto.Response, error) {
	return b.replyFrame(id, v, err, nil)
}

func (b *bridge) replyFrame(id uint64, v any, err error, bind func(proto.Response) func()) (proto.Response, error) {
	b.noteReply(id)
	resp := proto.Response{ID: id, OK: err == nil}
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			resp.Error = pe
		} else {
			resp.Error = &proto.Error{Code: "error", Message: err.Error()}
		}
	} else if v != nil {
		resp.Result, _ = json.Marshal(v)
	}
	if bind != nil {
		if progress := bind(resp); progress != nil {
			return resp, b.w.WriteJSONProgress(proto.TResponse, resp, progress)
		}
	}
	return resp, b.w.WriteJSON(proto.TResponse, resp)
}

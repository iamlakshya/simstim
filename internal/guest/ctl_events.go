package guest

import (
	"github.com/iamlakshya/simstim/internal/proto"
	"time"
)

// heartbeat sends a heartbeat on the ctl stream every heartbeatEvery,
// until the guest init ends.
func (g *Init) heartbeat(w *proto.Writer) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		if g.vm != nil {
			if claim := g.vm.memoryClaim(); claim != nil {
				if w.WriteJSON(proto.TEvent, proto.CtlEvent{Event: proto.EventMem, GuestMemoryV1: claim}) != nil {
					return
				}
			}
		}
		if w.WriteJSON(proto.TEvent, proto.CtlEvent{Event: proto.EventHeartbeat}) != nil {
			return
		}
		select {
		case <-t.C:
		case <-g.done:
			return
		}
	}
}

// event passes one of the holder's events to the supervisor.
func (g *Init) event(payload []byte) {
	g.mu.Lock()
	w := g.ctl
	g.mu.Unlock()
	if w != nil {
		_ = w.Write(proto.TEvent, payload)
	}
}

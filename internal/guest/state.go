package guest

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (g *Init) restoreState(a proto.CtlStartArgs) error {
	if g.vm != nil {
		return nil // actual VMPrepare restored the same protected generation
	}
	plan := files.StatePlan{Files: a.StateFiles}
	if err := plan.Validate(); err != nil {
		return err
	}
	if len(a.StateFiles) == 0 {
		return nil
	}
	if len(a.CaptureNonce) != 32 {
		return &proto.Error{Code: "wall_state", Message: "the state capture has no run nonce", Hint: "Resume the session with an updated helper."}
	}
	home := g.path(host.GuestHome)
	if err := files.RestoreFamilies(home, g.path(files.RestorePath), a.StateFiles); err != nil {
		return err
	}
	g.mu.Lock()
	g.stateFiles = append([]string(nil), a.StateFiles...)
	g.captureNonce = a.CaptureNonce
	g.stateSession = a.Session
	g.mu.Unlock()
	return nil
}

func (g *Init) captureState(clean bool) {
	if g.vm != nil {
		if err := g.vm.capture(clean); err != nil {
			g.log.Error("cannot capture VM agent state", "err", err)
		}
		return
	}
	g.mu.Lock()
	names := append([]string(nil), g.stateFiles...)
	nonce, session := g.captureNonce, g.stateSession
	g.mu.Unlock()
	if !clean || len(names) == 0 {
		return
	}
	if err := files.CaptureFamilies(g.path(host.GuestHome), g.path(files.CapturePath), names); err != nil {
		g.log.Error("cannot capture agent state", "err", err)
		return
	}
	p, _ := json.Marshal(proto.CtlEvent{Event: proto.EventStateReady, Session: session, CaptureNonce: nonce})
	g.event(p)
}

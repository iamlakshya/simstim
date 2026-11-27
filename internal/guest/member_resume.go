package guest

import (
	"encoding/json"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (g *Init) prepareMemberResume(raw []byte) (proto.CtlMemberPrepareResult, error) {
	var args proto.CtlMemberResumePrepareArgs
	fields, err := api.JSONObjectFields(raw, "version", "previous_run", "start")
	if err != nil || len(fields) != 3 || !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != 1 || args.PreviousRun == "" || args.Start.RunToken == "" || args.Start.RunToken == args.PreviousRun || !api.ValidSessionID(args.Start.Session) {
		return proto.CtlMemberPrepareResult{}, execError("invalid original member resume preparation")
	}
	if err := proto.CheckMemberPrepareFrames(proto.CtlSpawn, args.Start); err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	g.mu.Lock()
	old := g.members[args.Start.Session]
	busy := g.ended || g.lifetime == nil || g.lifetime.Err() != nil || !g.sessionPrepared || g.memberStarts[args.Start.Session] || old == nil || old.cmd == nil || old.cmd.Process == nil || old.runToken != args.PreviousRun || !old.agentEnded
	if pending := g.preparedMembers[args.Start.Session]; pending != nil && pending.state != memberConsumed && pending.state != memberFailed {
		busy = true
	}
	if busy {
		g.mu.Unlock()
		return proto.CtlMemberPrepareResult{}, execError("the original ended member cannot reserve a new run")
	}
	// This hold keeps the actual warm guest alive while the old holder joins.
	// It is local lifecycle ownership, never native host/runtime authority.
	g.commandHolds++
	if g.memberStarts == nil {
		g.memberStarts = make(map[string]bool)
	}
	g.memberStarts[args.Start.Session] = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.commandHolds--
		// A successful prepare owns its own marker. Every earlier failure leaves
		// the original holder reachable and releases only this retirement marker.
		if pending := g.preparedMembers[args.Start.Session]; pending == nil || pending.state == memberConsumed || pending.state == memberFailed {
			delete(g.memberStarts, args.Start.Session)
		}
		g.mu.Unlock()
		g.endIfEmpty()
	}()
	if g.vm != nil {
		if err := g.vm.retireMemberForResume(args.Start.Session, args.PreviousRun); err != nil {
			return proto.CtlMemberPrepareResult{}, err
		}
	}
	old.stop(0)
	if !old.exited() {
		return proto.CtlMemberPrepareResult{}, execError("the previous owned holder did not finish")
	}
	g.drainHolderEvents(old)
	old.closeEvents()
	<-old.eventsDone
	for _, path := range []string{old.files.Sock, old.files.Hook} {
		if err := removeRetiredGuestSocket(path); err != nil {
			return proto.CtlMemberPrepareResult{}, err
		}
	}
	g.mu.Lock()
	if g.ended || g.members[args.Start.Session] != old || old.runToken != args.PreviousRun {
		delete(g.memberStarts, args.Start.Session)
		g.mu.Unlock()
		return proto.CtlMemberPrepareResult{}, execError("the original member changed during retirement")
	}
	delete(g.members, args.Start.Session)
	delete(g.preparedMembers, args.Start.Session)
	delete(g.memberStarts, args.Start.Session)
	if g.holder == old {
		g.holder = nil
	}
	g.mu.Unlock()
	encoded, err := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: args.Start})
	if err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	return g.prepareMember(encoded)
}

func removeRetiredGuestSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return execError("the retired member endpoint is not its expected guest socket")
	}
	if err := os.Remove(path); err != nil {
		return execError("the retired guest member socket cannot be removed")
	}
	return nil
}

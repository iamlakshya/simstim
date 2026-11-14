package guest

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// abortPreparedMember releases only the original idle transaction. The SAME
// ordered ctl reader queues this behind a running prepare/Store/final launch.
func (g *Init) abortPreparedMember(raw []byte) (proto.CtlMemberAbortResult, error) {
	result := proto.CtlMemberAbortResult{Version: 1, State: "absent"}
	args, err := g.decodeMemberPreparation(raw)
	if err != nil {
		return result, err
	}
	fields, _ := api.JSONObjectFields(raw, "version", "target", "start")
	start, _ := api.JSONObjectFields(fields["start"])
	if _, present := start["prepared_claim"]; present && !api.InputNonce(args.Start.PreparedClaim).Valid() {
		return result, execError("invalid prepared member cleanup claim")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.preparedMembers[args.Start.Session]
	if p == nil {
		return result, nil
	}
	if p.target != args.Target || !sameMemberIntent(p.raw, args.Start) || args.Start.PreparedClaim != "" && args.Start.PreparedClaim != p.claim {
		return result, execError("prepared member cleanup names another immutable intent")
	}
	if g.ended || g.starting || p.state == memberPreparing || p.state == memberStoreRunning {
		result.State = "busy"
		return result, nil
	}
	if p.state == memberConsumed {
		result.State = "consumed"
		return result, nil
	}
	result.State = "aborted"
	if p.state == memberFailed {
		result.State = "failed"
	}
	p.state = memberFailed
	reserved := g.memberStarts[args.Start.Session]
	delete(g.memberStarts, args.Start.Session)
	if p.target == proto.CtlSpawn && reserved && g.pendingBoot > 0 {
		g.pendingBoot--
	}
	if p.target == proto.CtlStart && g.primary == args.Start.Session && g.holder == nil {
		// Only this unconsumed initial primary owns the pending boot barrier.
		g.bootAdmission = false
		g.pendingBoot = 0
	}
	return result, nil
}

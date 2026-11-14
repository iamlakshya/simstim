package supervisor

import (
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestTrustedPublicKindsCoverPendingACKAndOutliveMemberRemoval(t *testing.T) {
	if len(agents.Table) < 2 {
		t.Fatal("the canonical table has too few rows for the union fixture")
	}
	first, second := agents.Table[0].Kind, agents.Table[1].Kind
	ctl := &egressCtl{}
	ctl.setPublicInitial(egress.InitArgs{SessionID: "a2a2a2a2", AgentKinds: []string{first}, Secrets: map[string]string{"TOKEN": "never-retained"}})
	finish := ctl.beginPublicKind(second)
	beforeACK, err := ctl.publicInitial(host.Manifest{})
	if err != nil || !slices.Contains(beforeACK.AgentKinds, second) || beforeACK.Secrets != nil {
		t.Fatal("mint event before member ACK was under-sized or retained values", err)
	}
	finish(true)
	// Public projection belongs to the owned egress lifetime, even after a
	// member ended/was removed from active UI handles. No filesystem query.
	afterRemoval, err := ctl.publicInitial(host.Manifest{})
	if err != nil || !slices.Contains(afterRemoval.AgentKinds, first) || !slices.Contains(afterRemoval.AgentKinds, second) {
		t.Fatal("accepted trusted kind disappeared with its holder", err)
	}
	afterRemoval.AgentKinds[0] = "changed"
	own, _ := ctl.publicInitial(host.Manifest{})
	if slices.Contains(own.AgentKinds, "changed") {
		t.Fatal("public projection aliases its reader")
	}
}

func TestTrustedPublicKindRejectionAndCustomCommandDoNotGrantKinds(t *testing.T) {
	ctl := &egressCtl{}
	ctl.setPublicInitial(egress.InitArgs{SessionID: "a2a2a2a2"})
	finish := ctl.beginPublicKind(agents.Table[0].Kind)
	finish(false) // known negative ACK removes the prospective metadata
	ctl.beginPublicKind("")(true)
	ctl.beginPublicKind("unknown-custom-command")(true)
	args, err := ctl.publicInitial(host.Manifest{})
	if err != nil || len(args.AgentKinds) != 0 {
		t.Fatal("a rejected or unknown kind widened the public initial seed", err)
	}
	if _, err := (&egressCtl{}).publicInitial(host.Manifest{}); err == nil {
		t.Fatal("missing exact initial metadata produced guessed authority")
	}
}

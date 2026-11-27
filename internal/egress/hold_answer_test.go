package egress

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func heldAnswerArgs(t *testing.T, server *Server, host string, scope api.NetworkHoldScope) api.NetworkHoldActionArgs {
	t.Helper()
	server.policyMu.Lock()
	defer server.policyMu.Unlock()
	ticket, _, err := server.holds.attach(Request{Host: host, Port: 443}, host, server.now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return api.NetworkHoldActionArgs{Version: 1, Session: "answer2", Request: ticket.entry.meta.ID, Revision: ticket.entry.meta.Revision, Scope: scope, Maximum: api.NetworkHoldPublicOnly}
}

func TestActualHeldAnswerInstallsSensitiveOnceOrSessionWithoutPublishingItsTarget(t *testing.T) {
	for _, scope := range []api.NetworkHoldScope{api.NetworkHoldOnce, api.NetworkHoldSession} {
		t.Run(string(scope), func(t *testing.T) {
			server := newMemberServer(t, InitArgs{SessionID: "answer2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"KEY": "authored.example"}})
			args := heldAnswerArgs(t, server, "authored.example", scope)
			result, err := server.answerNetworkHold(memberJSON(t, args))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.MemoryOnly || len(result.RuleIDs) != 1 || !api.ValidNetworkHoldID(result.RuleIDs[0]) || strings.Contains(string(raw), "authored") {
				t.Fatal("sensitive answer disclosed target or fabricated a durable grant")
			}
			decision := server.currentEngine().evaluate(Request{Host: "authored.example", Port: 443}, nil, evalPrecheck)
			if !decision.Allow || decision.By != "live" {
				t.Fatal("actual answer did not reach the canonical compiled matcher with finite provenance")
			}
			if len(server.pendingNetworkSnapshot().Requests) != 0 {
				t.Fatal("successful answer left original hold open")
			}
		})
	}
}

func TestActualPublicSessionAnswerRequiresTheDurableWriterAndKeepsHold(t *testing.T) {
	server := newMemberServer(t, InitArgs{SessionID: "answer2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}})
	args := heldAnswerArgs(t, server, "authored.example", api.NetworkHoldSession)
	engine := server.currentEngine()
	_, err := server.answerNetworkHold(memberJSON(t, args))
	var refusal *proto.Error
	if !errors.As(err, &refusal) || refusal.Code != "network_publication_required" || server.currentEngine() != engine || len(server.pendingNetworkSnapshot().Requests) != 1 {
		t.Fatal("ordinary session answer pretended to persist in memory")
	}
}

func TestActualHeldAnswerRejectsStaleRevisionAndEveryPrivateLiteralMaximum(t *testing.T) {
	server := newMemberServer(t, InitArgs{SessionID: "answer2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}})
	args := heldAnswerArgs(t, server, "authored.example", api.NetworkHoldOnce)
	engine := server.currentEngine()
	args.Revision++
	if _, err := server.answerNetworkHold(memberJSON(t, args)); err == nil || server.currentEngine() != engine {
		t.Fatal("stale answer changed the canonical engine")
	}
	for _, maximum := range []api.NetworkHoldMaximum{api.NetworkHoldPublicOnly, api.NetworkHoldExistingPolicy} {
		args = heldAnswerArgs(t, server, "127.0.0.1", api.NetworkHoldOnce)
		args.Maximum = maximum
		if _, err := server.answerNetworkHold(memberJSON(t, args)); err == nil || server.currentEngine() != engine {
			t.Fatal("held answer created a private numeric exception")
		}
	}
}

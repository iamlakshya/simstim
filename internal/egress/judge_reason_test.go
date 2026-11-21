package egress

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func TestJudgeReasonUsesCompleteCredentialBeforePublicCap(t *testing.T) {
	for _, length := range []int{256, 1021} {
		secret := strings.Repeat("AbC", length/3) + strings.Repeat("z", length%3)
		server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"CREDENTIAL": secret}))
		ctx := judgeSourceContext(t, server)
		result, err := classify.SanitizeResult(ctx, classify.Result{Action: "deny", Reason: "\x1b" + secret + strings.Repeat("x", 220), Scope: "session"}, "once")
		if err != nil || !strings.HasPrefix(result.Reason, "[CREDENTIAL]") || strings.Contains(result.Reason, secret[:200]) || len(result.Reason) != 200 {
			t.Fatal("source scrub followed the reason cap", err)
		}
		server.classifier.close()
	}
}

func TestJudgeReasonRejectsReplacedSource(t *testing.T) {
	server, _ := classifierFixture(t, "1s", 20)
	ctx := judgeSourceContext(t, server)
	server.mu.Lock()
	next := *server.members
	server.members = &next
	server.mu.Unlock()
	result, err := classify.SanitizeResult(ctx, classify.Result{Action: "deny", Reason: "private reason", Scope: "once"}, "once")
	if err == nil || result != (classify.Result{}) {
		t.Fatal("replaced source published a reason")
	}
	server.classifier.close()
}

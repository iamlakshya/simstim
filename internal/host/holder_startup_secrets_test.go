package host

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPendingControlExpiredSecretsWaitCannotAcknowledgeLateDelivery(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	p.mu.Lock()
	// This is the real boundary where the timer fired but its waiter has
	// not reacquired the gate to clear waiting. Delivery must still refuse.
	p.want, p.waiting, p.ready = []string{"EXPECTED"}, true, true
	p.secretDeadline = time.Unix(1, 0)
	p.secretReady = make(chan struct{})
	p.mu.Unlock()
	values := secretsMsg{Env: map[string]api.Secret{"EXPECTED": "late-fixture"}}
	if _, err := HolderRequest(m.ID, "secrets", values, time.Second); err == nil {
		t.Fatal("an expired secrets wait acknowledged a late delivery")
	}
	if env := p.finishSecretsWait(); env != nil {
		t.Fatalf("expired wait committed a secret environment: %q", env)
	}
}

func TestPendingControlSecretsTimeoutDecisionKeepsAlreadyAcceptedDelivery(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	p.mu.Lock()
	p.want, p.waiting, p.ready = []string{"EXPECTED"}, true, true
	p.secretDeadline = time.Now().Add(time.Second)
	p.secretReady = make(chan struct{})
	p.mu.Unlock()
	values := secretsMsg{Env: map[string]api.Secret{"EXPECTED": "accepted-fixture"}}
	if _, err := HolderRequest(m.ID, "secrets", values, time.Second); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	// Delivery committed before the deadline. The timer branch may be
	// selected too, but finalisation must return exactly the accepted data.
	p.secretDeadline = time.Unix(1, 0)
	p.mu.Unlock()
	if env := p.finishSecretsWait(); strings.Join(env, "\n") != "EXPECTED=accepted-fixture" {
		t.Fatalf("timeout branch discarded an acknowledged delivery: %q", env)
	}
	if env := p.finishSecretsWait(); env != nil {
		t.Fatalf("secret result was consumed more than once: %q", env)
	}
}

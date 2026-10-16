package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestNewSessionGetsTheHostsSecrets(t *testing.T) {
	hs := home(t, 80, 24)
	hs.be.secrets = map[string]api.Secret{"ANTHROPIC_API_KEY": "sk-test-value"}
	hs.key("n", "enter")
	hasCall(t, hs.be, "secrets atlas-dev")
	// The fake records the value as fmt prints it: redacted.
	hasCall(t, hs.be, "new atlas-dev secret ANTHROPIC_API_KEY=[redacted]")
	if len(hs.be.attached) != 1 {
		t.Fatal("the session did not start")
	}
	for _, c := range hs.be.callList() {
		if strings.Contains(c, "sk-test-value") {
			t.Fatalf("a secret was printed: %s", c)
		}
	}
}

func TestNewSessionSecretError(t *testing.T) {
	hs := home(t, 80, 24)
	hs.be.secretErr = &client.UserError{Msg: "the command of the secret ANTHROPIC_API_KEY failed (exit code 1)", Hint: "Run the command of ANTHROPIC_API_KEY from config.toml yourself, to see what it says."}
	hs.key("n", "enter")
	for _, c := range hs.be.callList() {
		if strings.HasPrefix(c, "new atlas-dev") {
			t.Fatalf("a session started without its secret: %s", c)
		}
	}
	p := hs.m.prompt
	if p == nil || p.err == nil || p.starting {
		t.Fatalf("the error is not on the prompt: %+v", p)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "secret ANTHROPIC_API_KEY failed") {
		t.Fatalf("the error is not shown:\n%s", out)
	}
}

func TestMissingSecretsShow(t *testing.T) {
	be := labBackend()
	st := be.state["atlas-dev"]
	st.sessions[2].MissingSecrets = []string{"OPENAI_API_KEY"}
	hs := newHarness(t, be, 100, 24)
	hs.m.motion = false
	hs.key("enter", "down")
	if hs.m.sel.id != "s3" {
		t.Fatalf("selected %v", hs.m.sel)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "started without its secrets: OPENAI_API_KEY") {
		t.Fatalf("the launcher does not say the session has no secrets:\n%s", out)
	}
}

func (b *fakeBackend) SetupTrust(host string) map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.trust[host]
}

func (b *fakeBackend) RememberSetup(host, repo string, run bool) error {
	b.record("remember %s %s run=%v", host, repo, run)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trust == nil {
		b.trust = map[string]map[string]bool{}
	}
	if b.trust[host] == nil {
		b.trust[host] = map[string]bool{}
	}
	b.trust[host][repo] = run
	return nil
}

func (b *fakeBackend) Secrets(_ context.Context, h client.Host) (map[string]api.Secret, error) {
	b.record("secrets %s", h.Name)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.secrets, b.secretErr
}

// SessionSecrets fetches the named secrets from the same pretend config.
func (b *fakeBackend) SessionSecrets(_ context.Context, names []string) (map[string]api.Secret, error) {
	if len(names) == 0 {
		return nil, nil
	}
	b.record("secrets %v", names)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.secretErr != nil {
		return nil, b.secretErr
	}
	out := map[string]api.Secret{}
	for _, n := range names {
		v, ok := b.secrets[n]
		if !ok {
			return nil, &client.UserError{Msg: "no secret named " + n + " in config.toml", Hint: "Add it under [secrets]."}
		}
		out[n] = v
	}
	return out, nil
}

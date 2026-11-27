package client

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestOriginalRequestSettingsSurviveOnlyCancelledAttempt(t *testing.T) {
	isolateTypedSecretSettings(t)
	if err := os.WriteFile(settingsPath(), []byte("[agents]\ninline=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logical, endRequest := context.WithCancel(t.Context())
	defer endRequest()
	r := NewGrantRequest(false, false)
	base := WithGrantRequest(logical, r)
	defer r.Close()
	first, endAttempt := context.WithCancel(base)
	operation, err := r.borrowNewSettings(first)
	if err != nil {
		t.Fatal(err)
	}
	endAttempt()
	if err := operation.Revalidate(first); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled attempt retained permission for source IO", err)
	}
	second := WithGrantRequest(base, r)
	again, err := r.borrowNewSettings(second)
	if err != nil || again != operation {
		t.Fatal("setup/folder retry reopened or lost original settings FD", err)
	}
	endRequest()
	if _, err := r.borrowNewSettings(WithGrantRequest(t.Context(), r)); !errors.Is(err, context.Canceled) {
		t.Fatal("fresh context revived an ended original request", err)
	}
}

func TestEndingRequestCannotAcceptLateAnswerWhileCleanupIsIncomplete(t *testing.T) {
	h := h5GrantState(t)
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	question := allowRequestGrant(ctx, h, h5Policy().HostID, TierShared, Outbound{Kind: OutboundSecret, Name: "API_KEY"},
		Settings{SecretTiers: map[string]string{"API_KEY": "shared"}})
	var confirm *ConfirmError
	if !errors.As(question, &confirm) {
		t.Fatal("fixture did not produce an actual scoped question", question)
	}
	r.newSecrets = &newSecretPlan{busy: true}
	if err := r.Close(); err == nil {
		t.Fatal("busy resource unexpectedly closed")
	}
	if _, err := ConfirmGrant(ctx, question); err == nil {
		t.Fatal("incomplete resource cleanup left terminal answers active")
	}
	if _, err := r.borrowNewSettings(ctx); err == nil {
		t.Fatal("incomplete cleanup admitted another settings operation")
	}
	r.newSecrets.mu.Lock()
	r.newSecrets.busy = false
	r.newSecrets.mu.Unlock()
	if err := r.Close(); err != nil {
		t.Fatal("actual resource close could not be retried", err)
	}
}

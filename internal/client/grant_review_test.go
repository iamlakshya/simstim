package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func reviewGrantFixture(t *testing.T) (Host, SandboxPolicy, string, string) {
	t.Helper()
	h := h5GrantState(t)
	p := h5Policy()
	p.Ceiling, p.Secrets = TierOwn, []string{"API_KEY", "OTHER_KEY"}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(t.TempDir(), "first-read"), filepath.Join(t.TempDir(), "second-read")
	config := fmt.Sprintf("[secrets]\nAPI_KEY = %q\nOTHER_KEY = %q\n",
		"touch "+ShellWord(a)+"; printf generated-local-review-first",
		"touch "+ShellWord(b)+"; printf generated-local-review-second")
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, p, a, b
}

func TestReviewReadsOnlyRecordedSentNamesAndNeverPublishes(t *testing.T) {
	h, p, first, missing := reviewGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), NewReviewGrantRequest())
	values, err := FetchReviewSecrets(ctx, h, p.SessionID, p.Secrets, []string{"OTHER_KEY"}, false)
	if err != nil || len(values) != 1 || values["API_KEY"] != "generated-local-review-first" {
		t.Fatalf("local review: names=%d error=%v", len(values), err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("the selected local value was not read")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a name marked missing was read")
	}
	if rows, err := GrantsForHostContext(t.Context(), h, p.HostID); err != nil || len(rows) != 0 {
		t.Fatalf("local review changed outbound history: %+v %v", rows, err)
	}
	writes := 0
	c := &Conn{Host: h, hostID: p.HostID, Features: []string{"session.id", "secrets", "resume"}}
	c.w = proto.NewWriter(grantWriteFunc(func(b []byte) (int, error) { writes++; return len(b), nil }))
	if _, err := c.NewSession(ctx, NewArgs{Secrets: values}); err == nil {
		t.Fatal("review values entered a remote producer")
	}
	if _, err := c.Resume(ctx, ResumeArgs{ID: p.SessionID, Secrets: values}); err == nil {
		t.Fatal("review values entered a resume producer")
	}
	if writes != 0 {
		t.Fatal("a local review published a remote frame")
	}
	state, err := os.ReadFile(sandboxStatePath(h))
	if err != nil || bytes.Contains(state, []byte(values["API_KEY"])) {
		t.Fatal("local review stored a value in its creation state")
	}
}

func TestReviewChecksAllNamesBeforeAnyCommand(t *testing.T) {
	for _, mode := range []string{"unknown-name", "unknown-session", "bad-config", "missing-not-sent"} {
		t.Run(mode, func(t *testing.T) {
			h, p, first, second := reviewGrantFixture(t)
			id, names, missing := p.SessionID, p.Secrets, []string(nil)
			switch mode {
			case "unknown-name":
				names = []string{"API_KEY", "UNRECORDED_KEY"}
			case "unknown-session":
				id = "uvw234xy"
			case "bad-config":
				file, err := os.OpenFile(settingsPath(), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("[secret_tiers]\nUNRELATED = \"invalid\"\n")
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			case "missing-not-sent":
				missing = []string{"UNRECORDED_KEY"}
			}
			_, err := FetchReviewSecrets(t.Context(), h, id, names, missing, false)
			if err == nil {
				t.Fatal("an incomplete review was accepted")
			}
			for _, marker := range []string{first, second} {
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("a value command ran before the complete admission check")
				}
			}
		})
	}
}

func TestReviewRequestPinsSessionNamesAndCreation(t *testing.T) {
	h, p, _, second := reviewGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), NewReviewGrantRequest())
	if _, err := FetchReviewSecrets(ctx, h, p.SessionID, []string{"API_KEY"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchReviewSecrets(ctx, h, p.SessionID, p.Secrets, nil, false); err == nil {
		t.Fatal("a review retry enlarged its selected list")
	}
	if _, err := os.Stat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a changed selection read another value")
	}
	other := p
	other.SessionID, other.request = "uvw234xy", "another-request"
	if err := reserveSandbox(t.Context(), h, other); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchReviewSecrets(ctx, h, other.SessionID, []string{"API_KEY"}, nil, false); err == nil {
		t.Fatal("a review request changed its session")
	}
	if _, err := FetchReviewSecrets(WithGrantRequest(t.Context(), NewGrantRequest(false, false)), h, p.SessionID, nil, nil, false); err == nil {
		t.Fatal("a remote request changed into a local review")
	}
}

func TestReviewUsesLocalQuestionsAndFreshTier(t *testing.T) {
	h, p, first, _ := reviewGrantFixture(t)
	if err := SetTierContext(t.Context(), h.Name, TierShared, "", ""); err != nil {
		t.Fatal(err)
	}
	config := "[secrets]\nAPI_KEY = \"printf local-review-scoped\"\n[secret_tiers]\nAPI_KEY = \"shared\"\n"
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), NewReviewGrantRequest())
	_, err := FetchReviewSecrets(ctx, h, p.SessionID, []string{"API_KEY"}, nil, false)
	var q *ConfirmError
	if !errors.As(err, &q) || !strings.Contains(q.Prompt, "on this computer to review pulled files") || strings.Contains(strings.ToLower(q.Prompt), "send") {
		t.Fatalf("review question: %v", err)
	}
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a value was read before answering")
	}
	ctx, err = ConfirmGrant(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	values, err := FetchReviewSecrets(ctx, h, p.SessionID, []string{"API_KEY"}, nil, false)
	if err != nil || values["API_KEY"] != api.Secret("local-review-scoped") {
		t.Fatalf("confirmed local read: %v", err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf forbidden-now\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchReviewSecrets(ctx, h, p.SessionID, []string{"API_KEY"}, nil, false); err == nil {
		t.Fatal("an old answer bypassed the current secret tier")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := FetchReviewSecrets(cancelled, h, p.SessionID, []string{"API_KEY"}, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled review: %v", err)
	}
}

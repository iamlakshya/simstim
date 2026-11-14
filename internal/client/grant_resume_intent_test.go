package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestUnknownResumeCreationRefusesBeforeReadingOwnSecret(t *testing.T) {
	h := h5GrantState(t)
	marker := filepath.Join(t.TempDir(), "own-command-must-not-run")
	command := "touch " + ShellWord(marker) + "; printf generated-own-value"
	scopedMarker := filepath.Join(t.TempDir(), "scoped-command")
	scopedCommand := "touch " + ShellWord(scopedMarker) + "; printf generated-scoped-value"
	config := fmt.Sprintf("[secrets]\nAPI_KEY = %q\nSCOPED_KEY = %q\n[secret_tiers]\nSCOPED_KEY = \"throwaway\"\n", command, scopedCommand)
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"API_KEY"}, {"SCOPED_KEY", "API_KEY"}} {
		_, err := FetchSessionSecrets(t.Context(), h, "abc234de", names, false)
		var refusal *UserError
		if !errors.As(err, &refusal) || refusal.Code != "secret_intent_missing" {
			t.Fatalf("unknown creation fetched an own-tier secret: %v", err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an own-tier value command ran before all creation grants passed")
	}
	if _, err := os.Stat(scopedMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an earlier scoped command ran before a later own-tier refusal")
	}
	if values, err := FetchSessionSecrets(t.Context(), h, "abc234de", nil, false); err != nil || len(values) != 0 {
		t.Fatalf("missing creation blocked a no-secret resume: %v", err)
	}
	values, err := FetchSessionSecrets(t.Context(), h, "abc234de", []string{"SCOPED_KEY"}, false)
	if err != nil || string(values["SCOPED_KEY"]) != "generated-scoped-value" {
		t.Fatalf("normally allowed scoped resume did not work: %v", err)
	}
}

func TestRecordedProcessResumeUsesCreationExposureAndCurrentCeiling(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	p.Ceiling, p.CreationWall, p.Exposed = TierOwn, "process", []string{"API_KEY"}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf generated-process-value\"\nOTHER_KEY = \"printf must-not-read\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := FetchSessionSecrets(t.Context(), h, p.SessionID, []string{"API_KEY"}, false)
	if err != nil || string(values["API_KEY"]) != "generated-process-value" {
		t.Fatalf("recorded process exposure was not a supported resume: %v", err)
	}
	if _, err := FetchSessionSecrets(t.Context(), h, p.SessionID, []string{"OTHER_KEY"}, false); err == nil {
		t.Fatal("host-requested names enlarged creation exposure")
	}
	none, err := NewWallGrantRequest(false, false, "none", []string{"OTHER_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSessionSecrets(WithGrantRequest(t.Context(), none), h, p.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a weaker wall claim replaced local process creation authority")
	}
	if err := SetTierContext(t.Context(), h.Name, TierThrowaway, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSessionSecrets(t.Context(), h, p.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("saved exposure bypassed the host's lowered tier")
	}
}

func TestResumeOwnValuePublicationStillNeedsKnownCreation(t *testing.T) {
	h := h5GrantState(t)
	writes := 0
	connection := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"resume", "secrets"}}
	connection.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) {
		writes++
		return len(data), nil
	}))
	_, err := connection.Resume(t.Context(), ResumeArgs{ID: "abc234de", Secrets: map[string]api.Secret{"API_KEY": api.Secret("generated-resend")}})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "secret_intent_missing" || writes != 0 {
		t.Fatalf("unknown creation published an own-tier resend: writes=%d, error=%v", writes, err)
	}
}

func TestCreationPersistenceFailurePublishesNoNewFrame(t *testing.T) {
	h := h5GrantState(t)
	if err := os.MkdirAll(sandboxStatePath(h), 0o700); err != nil {
		t.Fatal(err)
	}
	writes := 0
	connection := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"session.id", "secrets"}}
	connection.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) {
		writes++
		return len(data), nil
	}))
	_, err := connection.NewSession(t.Context(), NewArgs{Secrets: map[string]api.Secret{"API_KEY": api.Secret("generated-unsent-value")}})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "grant_state" || writes != 0 {
		t.Fatalf("failed creation persistence published: writes=%d error=%v", writes, err)
	}
}

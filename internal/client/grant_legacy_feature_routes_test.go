package client

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRecordedProcessResumeKeepsLegacyAndRequiresCompleteModernCapabilities(t *testing.T) {
	for _, mode := range []struct {
		name     string
		features []string
		refuses  bool
		modern   bool
	}{
		{name: "neither"},
		{name: "spawn-only", features: []string{api.FeatureSpawn}, refuses: true},
		{name: "snapshot-only", features: []string{api.FeatureSandboxSnapshot}, refuses: true},
		{name: "both", features: []string{api.FeatureSpawn, api.FeatureSandboxSnapshot}, modern: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			h, parent, c, request := sharedGrantFixture(t)
			c.Features = append([]string{"resume", "secrets"}, mode.features...)
			ctx := WithGrantRequest(t.Context(), request)
			var snapshots, resumes int
			sharedGrantWire(t, c, func(req proto.Request) any {
				switch req.Op {
				case "sandbox.snapshot":
					snapshots++
					return sharedGrantSnapshot(parent.SessionID)
				case "resume":
					resumes++
					return ResumeResult{Session: Session{ID: parent.SessionID}}
				default:
					t.Fatalf("unexpected operation %q", req.Op)
					return nil
				}
			})
			err := c.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY"})
			if mode.refuses {
				var refused *UserError
				if !errors.As(err, &refused) || refused.Code != "helper_old" || snapshots != 0 || resumes != 0 {
					t.Fatalf("incomplete capabilities reached a remote operation: %v queries=%d resumes=%d", err, snapshots, resumes)
				}
				if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
					t.Fatal("a refused group silently fell back to local legacy fetching")
				}
				if _, err := c.Resume(ctx, ResumeArgs{ID: parent.SessionID}); err == nil || resumes != 0 {
					t.Fatal("a refused group published a legacy frame")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			values, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false)
			if err != nil || values["API_KEY"] != "fixture-value" {
				t.Fatalf("the supported route could not fetch its recorded exposure: %v", err)
			}
			result, err := c.Resume(ctx, ResumeArgs{ID: parent.SessionID, Secrets: values})
			wantSnapshots := 0
			if mode.modern {
				wantSnapshots = 1
			}
			if err != nil || result.Session.ID != parent.SessionID || resumes != 1 || snapshots != wantSnapshots {
				t.Fatalf("wrong route: error=%v queries=%d resumes=%d", err, snapshots, resumes)
			}
		})
	}
}

func TestStandaloneProcessFetchCannotDonateItsRequestToModernResume(t *testing.T) {
	h, parent, modern, request := sharedGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), request)
	// This local getter has no Conn and retains the established creation,
	// ceiling and exposure checks. It cannot discover helper capabilities.
	values, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false)
	if err != nil || values["API_KEY"] != "fixture-value" {
		t.Fatalf("the genuine local compatibility path stopped working: %v", err)
	}
	writes := 0
	sharedGrantWire(t, modern, func(proto.Request) any {
		writes++
		return sharedGrantSnapshot(parent.SessionID)
	})
	if _, err := modern.Resume(ctx, ResumeArgs{ID: parent.SessionID, Secrets: values}); err == nil || writes != 0 {
		t.Fatalf("a local legacy request gained modern group authority: %v frames=%d", err, writes)
	}
	if err := modern.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY"}); err == nil || writes != 0 {
		t.Fatal("an old local selection was upgraded by a later helper check")
	}
}

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func machineJudgeValueFixtureV1(t *testing.T, minimum string, expose bool) (context.Context, *newSecretPlan, NewArgs, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the classifier marker uses a POSIX shell")
	}
	host := machineValueHostFixtureV1(t)
	marker := filepath.Join(t.TempDir(), "judge-machine-value")
	command := "printf synthetic-machine-judge; printf x >> " + ShellQuote(marker)
	config := "[classifier]\njudge='model'\nmodel='test-model'\nendpoint='https://model.example/v1/chat/completions'\nkey='KEY'\n[secrets.KEY]\nrun=" + fmt.Sprintf("%q", command) + "\nhosts=['model.example']\nheader='Authorization'\nformat='Bearer {value}'\non_hosts=['box']\n[secret_tiers]\nKEY=" + fmt.Sprintf("%q", minimum) + "\n"
	writeRetainedImagePolicyFixture(t, config)
	conn := finishGeneratedTransportFixture(t, host, strings.Repeat("a", 32), "session.id", api.FeatureBoundSecrets, api.FeatureJudgeKeyV1)
	request := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), request)
	t.Cleanup(func() {
		if err := request.Close(); err != nil {
			t.Error(err)
		}
	})
	var exposed []string
	if expose {
		exposed = []string{"KEY"}
	}
	if err := request.BindNewWall(ctx, "gvisor", exposed); err != nil {
		t.Fatal(err)
	}
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	intent := &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	selection := &LocalAgentSelection{host: host, kind: "shell"}
	if expose {
		selection.names = []string{"KEY"}
		intent.Modes["KEY"] = api.SecretModeExposed
	}
	plan := &newSecretPlan{conn: conn, operation: operation, selection: selection, intent: intent}
	return ctx, plan, NewArgs{Wall: "gvisor"}, marker
}

func TestMachineClassifierAlternativePathNeedsSharedExposureAndOwnConsent(t *testing.T) {
	for _, test := range []struct {
		minimum string
		expose  bool
		code    string
	}{{"own", true, "tier_refused"}, {"shared", false, "machine_secret_exposure"}, {"shared", true, ""}} {
		t.Run(fmt.Sprint(test.minimum, test.expose), func(t *testing.T) {
			ctx, plan, args, marker := machineJudgeValueFixtureV1(t, test.minimum, test.expose)
			err := prepareTrustedJudgeNewV1(ctx, plan, args)
			if _, markerErr := os.Stat(marker); !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatal("classifier ran before machine exposure preflight")
			}
			if test.code != "" {
				var userErr *UserError
				if !errors.As(err, &userErr) || userErr.Code != test.code {
					t.Fatalf("classifier preflight=%v", err)
				}
				return
			}
			var question *ConfirmError
			if !errors.As(err, &question) {
				t.Fatalf("raised tier bypassed classifier owner consent: %v", err)
			}
			ctx, err = ConfirmGrant(ctx, err)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareTrustedJudgeNewV1(ctx, plan, args); err != nil {
				t.Fatal(err)
			}
			settings, err := plan.operation.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			guest, err := fetchNewGuestValuesV1(ctx, plan, settings, plan.selection.names, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := fetchTrustedJudgeNewV1(ctx, plan, guest, false); err != nil {
				t.Fatal(err)
			}
			count, err := os.ReadFile(marker)
			if err != nil || string(count) != "x" {
				t.Fatalf("classifier/primary reads=%q, %v", count, err)
			}
			if plan.intent.Modes["KEY"] != api.SecretModeExposed {
				t.Fatal("classifier value was hidden behind an unrecorded exposure")
			}
		})
	}
}

func TestMachineClassifierCancelledConsentDoesNotReadEitherRole(t *testing.T) {
	ctx, plan, args, marker := machineJudgeValueFixtureV1(t, "shared", true)
	question := prepareTrustedJudgeNewV1(ctx, plan, args)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ConfirmGrant(cancelled, question); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled classifier answer=%v", err)
	}
	var stillPending *ConfirmError
	if err := prepareTrustedJudgeNewV1(ctx, plan, args); !errors.As(err, &stillPending) {
		t.Fatal("cancelled classifier answer approved the live parent request")
	}
	if err := fetchTrustedJudgeNewV1(cancelled, plan, nil, false); err == nil {
		t.Fatal("cancelled classifier consent fetched a value")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled classifier consent ran its command")
	}
}

func TestMachineClassifierUnusedExposureDoesNotInventPrimaryRecording(t *testing.T) {
	ctx, plan, args, marker := machineJudgeValueFixtureV1(t, "shared", true)
	plan.selection.names = nil
	delete(plan.intent.Modes, "KEY")
	err := prepareTrustedJudgeNewV1(ctx, plan, args)
	var userErr *UserError
	if !errors.As(err, &userErr) || userErr.Code != "machine_secret_exposure" {
		t.Fatalf("unused exposure=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unrecorded classifier exposure ran its command")
	}
}

func TestMachineClassifierCannotMoveOriginalAnswerToConnectionOrReadd(t *testing.T) {
	ctx, plan, args, marker := machineJudgeValueFixtureV1(t, "shared", true)
	question := prepareTrustedJudgeNewV1(ctx, plan, args)
	ctx, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTrustedJudgeNewV1(ctx, plan, args); err != nil {
		t.Fatal(err)
	}
	other := finishGeneratedTransportFixture(t, plan.conn.Host, plan.conn.HostID(), "session.id", api.FeatureBoundSecrets, api.FeatureJudgeKeyV1)
	borrowed := &newSecretPlan{conn: other, operation: plan.operation, selection: plan.selection, intent: plan.intent}
	if err := fetchTrustedJudgeNewV1(ctx, borrowed, nil, false); err == nil {
		t.Fatal("another connection borrowed the classifier answer")
	}
	host := plan.conn.Host
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host rm", Was: &host}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host add", Now: &host, Tier: TierOwn, HostID: plan.conn.HostID()}); err != nil {
		t.Fatal(err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, plan, nil, false); err == nil {
		t.Fatal("re-added host reused old classifier permission")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale classifier permission ran its value command")
	}
}

func TestMachineClassifierResumeUsesOriginalCreationExposure(t *testing.T) {
	host := Host{Name: "box", Machine: &Machine{Provider: "e2b", ID: "classifier-resume", Session: "work"}}
	settings := DefaultSettings()
	settings.SecretTiers = map[string]string{"KEY": "shared"}
	plan := &trustedJudgePlanV1{conn: &Conn{Host: host}, family: grantOpResume, name: "KEY", creation: &SandboxPolicy{CreationWall: "gvisor"}}
	request, err := NewWallGrantRequest(false, false, "gvisor", []string{"KEY"})
	if err != nil {
		t.Fatal(err)
	}
	defer request.Close()
	ctx := WithGrantRequest(t.Context(), request)
	if err := plan.checkMachineExposureV1(ctx, settings); err == nil {
		t.Fatal("a new request exposure replaced immutable creation facts")
	}
	plan.creation.Exposed = []string{"KEY"}
	if err := plan.checkMachineExposureV1(ctx, settings); err != nil {
		t.Fatal(err)
	}
	plan.creation.FutureDelivery = true
	if err := plan.checkMachineExposureV1(ctx, settings); err == nil {
		t.Fatal("future creation metadata authorised a classifier value")
	}
}

func TestMachineAlertTokenCannotUseRaisedSavedTier(t *testing.T) {
	host := Host{Name: "box", Machine: &Machine{Provider: "e2b", ID: "alert-source", Session: "work"}}
	cfg := AlertsConfig{}
	// The existing credential classifier supplies the pending token label.
	cfg.Destinations = []api.AlertDestination{{Type: "ntfy", URL: "https://alerts.example/topic"}}
	if err := checkAlertGrants(host, TierOwn, cfg, true, DefaultSettings()); err == nil {
		t.Fatal("raised tier authorised a provider-machine alert token")
	}
}

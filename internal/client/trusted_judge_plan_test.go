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

func trustedJudgeNewFixtureV1(t *testing.T, wall string, shared, expose bool, mutate ...func(string) string) (context.Context, *newSecretPlan, NewArgs, string) {
	t.Helper()
	h := h5GrantState(t)
	marker := filepath.Join(t.TempDir(), "judge-command-count")
	command := "printf synthetic-judge-key; printf x >> " + ShellQuote(marker)
	if runtime.GOOS == "windows" {
		command = `<nul set /p "=synthetic-judge-key" & <nul set /p "=x" >> "` + marker + `" & exit /b 0`
	}
	minimum := "own"
	if shared {
		minimum = "shared"
		if err := SetTierContext(t.Context(), h.Name, TierShared, strings.Repeat("a", 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	config := "[classifier]\njudge='model'\nmodel='test-model'\nendpoint='https://model.example/v1/chat/completions'\nkey='KEY'\n[secrets.KEY]\nrun=" + fmt.Sprintf("%q", command) + "\nhosts=['model.example']\nheader='Authorization'\nformat='Bearer {value}'\non_hosts=['box']\n[secret_tiers]\nKEY='" + minimum + "'\n"
	for _, change := range mutate {
		config = change(config)
	}
	writeRetainedImagePolicyFixture(t, config)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", api.FeatureBoundSecrets, api.FeatureJudgeKeyV1)
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	var exposed []string
	if expose {
		exposed = []string{"KEY"}
	}
	if err := r.BindNewWall(ctx, wall, exposed); err != nil {
		t.Fatal(err)
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := NewArgs{Wall: wall}
	plan := &newSecretPlan{conn: c, operation: operation, selection: &LocalAgentSelection{host: h, kind: "shell"}, intent: &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}}
	return ctx, plan, a, marker
}

func TestTrustedJudgeNewFetchesOnceIntoProtectedRoleOnly(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false)
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	if _, guest := p.intent.Modes["KEY"]; guest || len(p.selection.names) != 0 || len(p.intent.Binds) != 0 {
		t.Fatal("classifier reference created a guest environment or sentinel role")
	}
	for range 2 {
		if err := fetchTrustedJudgeNewV1(ctx, p, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	args.SecretDelivery = p.intent
	actual, err := applyTrustedJudgeNewV1(ctx, p, args)
	if err != nil || actual.ProtectedTrustedSecretValues["KEY"] != api.Secret("synthetic-judge-key") || len(actual.Secrets) != 0 || len(actual.SecretDelivery.Modes) != 0 || len(actual.SecretDelivery.TrustedUses) != 1 {
		t.Fatal("trusted value entered ordinary guest delivery", err)
	}
	count, err := os.ReadFile(marker)
	if err != nil || string(count) != "x" {
		t.Fatal("trusted retry ran another value command", err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if fmt.Sprintf(verb, trustedJudgePlanOfV1(ctx)) != api.Redacted || fmt.Sprintf(verb, grantRequestOf(ctx)) != api.Redacted {
			t.Fatal("private judge plan or public request formatting disclosed values")
		}
	}
	actual.ProtectedTrustedSecretValues["KEY"] = "caller replacement"
	if _, err := applyTrustedJudgeNewV1(ctx, p, actual); err == nil {
		t.Fatal("caller replaced original protected values")
	}
}

func TestTrustedJudgeAndExplicitGuestRoleShareOneOriginalFetch(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false)
	p.selection.names = []string{"KEY"}
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := FetchSecrets(ctx, settings, []string{"KEY"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, p, guest, false); err != nil {
		t.Fatal(err)
	}
	plan := trustedJudgePlanOfV1(ctx)
	if plan.values["KEY"] != guest["KEY"] {
		t.Fatal("separately authorised roles selected different values")
	}
	count, err := os.ReadFile(marker)
	if err != nil || string(count) != "x" {
		t.Fatal("shared name was fetched twice", err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, p, map[string]api.Secret{"KEY": "replacement-after-retry"}, false); err == nil {
		t.Fatal("retry adopted a second value for a separately authorised shared name")
	}
}

func TestTrustedJudgeProcessOwnQuestionCannotUseGuestExposureAnswer(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "process", false, true)
	question := prepareTrustedJudgeNewV1(ctx, p, args)
	var scoped *scopedGrantConfirmation
	if !errors.As(question, &scoped) || scoped.scope.kind != OutboundTrustedJudgeOwnV1 {
		t.Fatal("guest exposure donated an own-tier judge permission", question)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("own-risk question fetched the key", err)
	}
	ctx, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, p, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, guest := p.intent.Modes["KEY"]; guest {
		t.Fatal("own-risk answer delivered the key to the guest")
	}
}

func TestTrustedJudgeSharedQuestionIsIndependentOfGuestQuestion(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", true, false)
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guestQuestion := allowRequestGrant(ctx, p.conn.Host, p.conn.HostID(), TierShared, Outbound{Kind: OutboundSecret, Name: "KEY"}, settings)
	ctx, err = ConfirmGrant(ctx, guestQuestion)
	if err != nil {
		t.Fatal(err)
	}
	question := prepareTrustedJudgeNewV1(ctx, p, args)
	var scoped *scopedGrantConfirmation
	if !errors.As(question, &scoped) || scoped.scope.kind != outboundTrustedJudgeV1 {
		t.Fatal("ordinary Shared answer donated a trusted judge permission", question)
	}
	ctx, err = ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("independent questions fetched a value", err)
	}
}

func TestTrustedJudgeChangedSourceAndCloseRefuseBeforeFetch(t *testing.T) {
	for _, change := range []string{"settings", "close"} {
		t.Run(change, func(t *testing.T) {
			ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false)
			if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
				t.Fatal(err)
			}
			if change == "settings" {
				writeRetainedImagePolicyFixture(t, "# changed original settings\n")
			} else if err := grantRequestOf(ctx).Close(); err != nil {
				t.Fatal(err)
			}
			if err := fetchTrustedJudgeNewV1(ctx, p, nil, false); err == nil {
				t.Fatal("retired source fetched a key")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("retired source ran a command", err)
			}
		})
	}
}

func TestTrustedJudgeEndpointRequiresBoundHTTPSAndAllowsAnonymousLoopback(t *testing.T) {
	for _, raw := range []string{"https://user@model.example/v1/chat/completions", "https://model.example/v1/chat/completions?key=hidden", "http://model.example/v1/chat/completions", "https://model.example:8443/v1/chat/completions"} {
		if _, err := trustedJudgeEndpointV1(raw, "KEY"); err == nil {
			t.Fatal("configured key accepted an unbound or decorated endpoint")
		}
	}
	if authority, err := trustedJudgeEndpointV1("http://127.0.0.1:11434/v1/chat/completions", ""); err != nil || authority != "" {
		t.Fatal("anonymous local model selected a key", err)
	}
	if _, err := trustedJudgeEndpointV1("http://untrusted.example/v1/chat/completions", ""); err == nil {
		t.Fatal("anonymous HTTP endpoint was not local")
	}
}

func TestTrustedJudgeWrongKindHostBindAndFeatureRefuseBeforeFetch(t *testing.T) {
	for _, name := range []string{"kind", "on_hosts", "binding", "feature"} {
		t.Run(name, func(t *testing.T) {
			mutation := func(config string) string { return config }
			switch name {
			case "kind":
				mutation = func(config string) string {
					config = strings.Replace(config, "[secrets.KEY]", "[secrets.KEY]\nkind='ssh-key'", 1)
					return strings.Replace(config, "hosts=['model.example']\nheader='Authorization'\nformat='Bearer {value}'\n", "", 1)
				}
			case "on_hosts":
				mutation = func(config string) string { return strings.Replace(config, "on_hosts=['box']", "on_hosts=[]", 1) }
			case "binding":
				mutation = func(config string) string {
					return strings.Replace(config, "hosts=['model.example']", "hosts=['another.example']", 1)
				}
			}
			ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false, mutation)
			if name == "feature" {
				p.conn.Features = []string{"session.id", api.FeatureBoundSecrets}
			}
			if err := prepareTrustedJudgeNewV1(ctx, p, args); err == nil {
				t.Fatal("invalid classifier key intake passed")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid intake ran a value command", err)
			}
		})
	}
}

func TestTrustedJudgeAnonymousLocalModelFetchesNothing(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false, func(config string) string {
		config = strings.Replace(config, "key='KEY'", "key=''", 1)
		return strings.Replace(config, "https://model.example/v1/chat/completions", "http://127.0.0.1:11434/v1/chat/completions", 1)
	})
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, p, nil, false); err != nil {
		t.Fatal(err)
	}
	args.SecretDelivery = p.intent
	actual, err := applyTrustedJudgeNewV1(ctx, p, args)
	if err != nil || len(actual.ProtectedTrustedSecretValues) != 0 || len(actual.SecretDelivery.TrustedUses) != 0 {
		t.Fatal("anonymous model selected a credential", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("anonymous model ran an unused value command", err)
	}
}

func TestTrustedJudgeNewPreflightKeepsCurrentTierBeforeAnyValue(t *testing.T) {
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false)
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), p.conn.Host.Name, TierShared, p.conn.HostID(), ""); err != nil {
		t.Fatal(err)
	}
	if err := checkTrustedJudgeNewBeforeValuesV1(ctx, p); err == nil {
		t.Fatal("pre-value judge check donated the previous host tier")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight tier refusal ran a value command", err)
	}
}

func TestTrustedJudgeNewRetryRetainsEarlierGuestCommandSuccess(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "allow-second-command")
	second := "if [ -f " + ShellQuote(gate) + " ]; then printf second-value; else exit 7; fi"
	if runtime.GOOS == "windows" {
		second = `if exist "` + gate + `" (<nul set /p "=second-value" & exit /b 0) else (exit /b 7)`
	}
	ctx, p, args, marker := trustedJudgeNewFixtureV1(t, "gvisor", false, false, func(config string) string {
		return config + "[secrets.SECOND]\nrun=" + fmt.Sprintf("%q", second) + "\nhosts=['model.example']\nheader='Authorization'\nformat='Bearer {value}'\non_hosts=['box']\n"
	})
	p.selection.names = []string{"KEY", "SECOND"}
	if err := prepareTrustedJudgeNewV1(ctx, p, args); err != nil {
		t.Fatal(err)
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetchNewGuestValuesV1(ctx, p, settings, p.selection.names, false); err == nil {
		t.Fatal("controlled later guest command unexpectedly succeeded")
	}
	if p.values["KEY"] != api.Secret("synthetic-judge-key") || p.fetched || p.guestFetched {
		t.Fatal("later failure lost earlier original success or marked the whole plan ready")
	}
	if err := os.WriteFile(gate, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	guest, err := fetchNewGuestValuesV1(ctx, p, settings, p.selection.names, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetchTrustedJudgeNewV1(ctx, p, guest, false); err != nil {
		t.Fatal(err)
	}
	count, err := os.ReadFile(marker)
	if err != nil || string(count) != "x" || guest["KEY"] != p.values["KEY"] || trustedJudgePlanOfV1(ctx).values["KEY"] != guest["KEY"] || guest["SECOND"] != api.Secret("second-value") {
		t.Fatal("retry reran or replaced the original shared guest/judge value", err)
	}
}

func TestClassifierReferenceDoesNotSelectOrdinaryGuestNames(t *testing.T) {
	ctx, p, args, _ := trustedJudgeNewFixtureV1(t, "gvisor", false, false)
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := retainedNewImagePolicyLayersV1(ctx, p.operation, p.conn.Host, args)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectNewAgentSecrets(settings, p.conn.Host, NewArgs{Agent: "shell", Argv: []string{"sh"}}, layers)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Names()) != 0 {
		t.Fatal("classifier key reference entered the actual ordinary guest selector")
	}
}

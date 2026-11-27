package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func coldNamedKeyFixture(t *testing.T, kind, onHosts string, shared bool) (context.Context, *newSecretPlan, NewArgs, string) {
	t.Helper()
	h := h5GrantState(t)
	marker := filepath.Join(t.TempDir(), "key-command-count")
	command := "printf synthetic-key; printf x >> " + ShellQuote(marker)
	config := "[secrets.KEY]\nrun = " + fmt.Sprintf("%q", command) + "\nkind = " + fmt.Sprintf("%q", kind) + "\non_hosts = " + onHosts + "\n"
	if shared {
		config += "[secrets.OTHER]\nrun='printf unused'\nkind='ssh-key'\n[secret_tiers]\nKEY='shared'\nOTHER='shared'\n"
		if err := SetTierContext(t.Context(), h.Name, TierShared, strings.Repeat("a", 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	writeRetainedImagePolicyFixture(t, config)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", api.FeatureNamedSSHKeysV1, api.FeatureBoundSecrets)
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.BindNewWall(ctx, "process", nil); err != nil {
		t.Fatal(err)
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args := NewArgs{Wall: "process", Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"KEY"}}}}}}
	plan := &newSecretPlan{conn: c, operation: operation, selection: &LocalAgentSelection{host: h, kind: "codex"}, layers: api.CloneLayers(args.Layers)}
	return ctx, plan, args, marker
}

func TestOriginalNamedColdKeysStaySeparateAndFetchOnce(t *testing.T) {
	ctx, plan, args, marker := coldNamedKeyFixture(t, "ssh-key", "['box']", false)
	keys, err := prepareNewNamedSSHKeys(ctx, plan, args)
	if err != nil {
		t.Fatal(err)
	}
	plan.namedKeys = keys
	if err := keys.fetch(ctx, plan, false); err != nil {
		t.Fatal(err)
	}
	if err := keys.fetch(ctx, plan, false); err != nil {
		t.Fatal(err)
	}
	carrier, err := keys.arguments(nil)
	if err != nil || !carrier.Valid() || carrier.Values["KEY"] != api.Secret("synthetic-key") || len(plan.values) != 0 || len(args.Secrets) != 0 {
		t.Fatal("cold key entered an ordinary value map or lost original carrier", err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if fmt.Sprintf(verb, keys) != api.Redacted {
			t.Fatal("private named key plan formatting was not redacted")
		}
	}
	if fmt.Sprintf("%+v", carrier) != api.Redacted {
		t.Fatal("named carrier diagnostic disclosed private values")
	}
	if err := keys.beforePhysical(ctx, plan, carrier); err == nil {
		t.Fatal("missing original creation reached physical key ledger")
	}
	carrier.Values["KEY"] = "caller replacement"
	if _, err := keys.arguments(carrier); err == nil {
		t.Fatal("caller replaced retained key bytes")
	}
	settings, err := plan.operation.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSecrets(ctx, settings, []string{"KEY"}, false); err == nil {
		t.Fatal("SSH key was exported through ordinary environment secret helper")
	}
	count, err := os.ReadFile(marker)
	if err != nil || string(count) != "x" {
		t.Fatal("retry re-ran the key command", string(count), err)
	}
	if err := keys.checkAcknowledged(&api.NamedSSHKeyViewV1{V: 1, Installed: []string{"KEY"}, NextStart: []string{"KEY"}}); err != nil {
		t.Fatal(err)
	}
	if err := keys.checkAcknowledged(&api.NamedSSHKeyViewV1{V: 1, Installed: []string{"OTHER"}}); err == nil {
		t.Fatal("another installed name completed original key ACK")
	}
}

func TestNamedColdKeyKindHostAndUnwalledRefuseBeforeCommand(t *testing.T) {
	for _, caseName := range []string{"kind", "host", "unwalled", "ordinary overlap"} {
		t.Run(caseName, func(t *testing.T) {
			kind, hosts := "ssh-key", "['box']"
			if caseName == "kind" {
				kind = ""
			}
			if caseName == "host" {
				hosts = "[]"
			}
			ctx, plan, args, marker := coldNamedKeyFixture(t, kind, hosts, false)
			if caseName == "unwalled" {
				args.Wall = "none"
			}
			if caseName == "ordinary overlap" {
				plan.selection.names = []string{"KEY"}
			}
			if _, err := prepareNewNamedSSHKeys(ctx, plan, args); err == nil {
				t.Fatal("invalid cold key source passed")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal ran a key command", err)
			}
		})
	}
	if err := refuseUnwalledNamedSSHKeys(NewArgs{Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"KEY"}}}}}}); err == nil {
		t.Fatal("unwalled source metadata was silently dropped")
	}
}

func TestNamedColdQuestionRetainsOriginalNamesAndSource(t *testing.T) {
	ctx, plan, args, marker := coldNamedKeyFixture(t, "ssh-key", "['box']", true)
	_, question := prepareNewNamedSSHKeys(ctx, plan, args)
	var confirmation *ConfirmError
	if !errors.As(question, &confirmation) || confirmation.Grant.Kind != OutboundSSHKey {
		t.Fatal("shared key did not ask its own scoped question", question)
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	original := r.namedNew
	r.mu.Unlock()
	changed := args
	changed.Layers = api.CloneLayers(args.Layers)
	changed.Layers[0].Sandbox.SSHKeys = []string{"OTHER"}
	if _, err := prepareNewNamedSSHKeys(ctx, plan, changed); err == nil {
		t.Fatal("question retry expanded original key names")
	}
	ctx, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	again, err := prepareNewNamedSSHKeys(ctx, plan, args)
	if err != nil || again != original {
		t.Fatal("answer renewed original key source", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("question preparation ran a key command", err)
	}
	writeRetainedImagePolicyFixture(t, "# original key source changed\n")
	if err := again.fetch(ctx, plan, false); err == nil {
		t.Fatal("changed settings reopened key source")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed source still ran key command", err)
	}
}

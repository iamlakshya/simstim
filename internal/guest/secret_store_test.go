package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func storeAgentFixture(t *testing.T, rows []agents.SecretBinding) string {
	t.Helper()
	old := agents.Table
	const kind = "store-fixture"
	agents.Table = append(slices.Clone(old), agents.Agent{Kind: kind, Secrets: rows})
	t.Cleanup(func() { agents.Table = old })
	return kind
}

func requireSecretStoreError(t *testing.T, err error, private ...string) {
	t.Helper()
	var failure *proto.Error
	if !errors.As(err, &failure) || failure.Code != "secret_store" || failure.Hint == "" {
		t.Fatalf("Store error = %v, want secret_store with a hint", err)
	}
	for _, value := range private {
		if strings.Contains(failure.Message+failure.Hint, value) {
			t.Fatal("Store error echoed a value, command, environment or output")
		}
	}
}

func TestSecretStoreUsesCanonicalSelectedRowsAndStdinOnlyEnvironment(t *testing.T) {
	row := agents.SecretBinding{Env: "STORE_KEY", Store: []string{"tool", "login", "literal;$VALUE"}}
	kind := storeAgentFixture(t, []agents.SecretBinding{row, {Env: "NO_STORE"}})
	base := []string{"HOME=/home/agent", "PATH=/usr/bin:/bin", "SSL_CERT_FILE=/run/ca.pem", "HTTP_PROXY=http://127.0.0.1:3128"}
	sentinels := map[string]api.Secret{"STORE_KEY": "sentinel-exact", "NO_STORE": "other-sentinel"}
	plans, env, err := prepareSecretStores("store234", kind, "/work/app", base, sentinels)
	if err != nil || len(plans) != 1 || plans[0].name != "STORE_KEY" || plans[0].sentinel != sentinels["STORE_KEY"] || !reflect.DeepEqual(plans[0].argv, row.Store) {
		t.Fatalf("Store selection changed canonical data: %+v, %v", plans, err)
	}
	if !reflect.DeepEqual(env, base) || len(base) != 4 {
		t.Fatal("Store changed the owned base environment or added non-sentinel credentials")
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "STORE_KEY=") || strings.HasPrefix(entry, "NO_STORE=") {
			t.Fatal("bound sentinel was added to Store environment")
		}
	}
	row.Store[0], base[0] = "changed", "HOME=/changed"
	sentinels["STORE_KEY"] = "changed"
	if plans[0].argv[0] != "tool" || plans[0].sentinel != "sentinel-exact" || env[0] != "HOME=/home/agent" {
		t.Fatal("Store plan retained mutable caller-owned metadata")
	}
}

func TestSecretStoreDoesNothingForAbsentExposedAndOtherKindNames(t *testing.T) {
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "STORE_KEY", Store: []string{"unavailable-tool"}}, {Env: "NO_STORE"}})
	var guest *Init
	for _, tc := range []struct {
		kind      string
		sentinels map[string]api.Secret
	}{
		{kind, nil}, {kind, map[string]api.Secret{"NO_STORE": "sentinel"}},
		{"custom-command", map[string]api.Secret{"STORE_KEY": "sentinel"}},
	} {
		// An exposed value is not in the bound-sentinel projection. No
		// command is possible here, even with an unusable Init receiver.
		if err := guest.storeBoundSecrets(context.Background(), "store234", tc.kind, "/work/app", nil, tc.sentinels); err != nil {
			t.Fatalf("absent Store unexpectedly needed an executor: %v", err)
		}
	}
}

func TestSecretStorePrevalidatesEveryRowAndRejectsAmbiguousBaseEnvironment(t *testing.T) {
	const private = "must-not-appear-private-marker"
	valid := agents.SecretBinding{Env: "STORE_KEY", Store: []string{"tool", "login"}}
	for _, rows := range [][]agents.SecretBinding{
		{valid, {Env: "BAD-NAME"}}, {valid, valid},
		{valid, {Env: "LATER", Store: []string{"", private}}},
		{valid, {Env: "LATER", Store: []string{"tool", private + "\x00"}}},
	} {
		plans, _, err := planSecretStores(rows, map[string]api.Secret{"STORE_KEY": "sentinel"})
		requireSecretStoreError(t, err, private)
		if plans != nil {
			t.Fatal("invalid later metadata returned runnable partial Store plans")
		}
	}
	credentials := map[string]bool{"STORE_KEY": true, "EXPOSED_KEY": true}
	for _, base := range [][]string{
		{"HOME=/home/agent", "HOME=/other"}, {"MALFORMED"},
		{"STORE_KEY=" + private}, {"EXPOSED_KEY=" + private},
		{"1INVALID=" + private}, {"HOME=" + private + "\x00"},
	} {
		_, err := secretStoreEnvironment(base, map[string]api.Secret{"STORE_KEY": "sentinel"}, credentials, "STORE_KEY")
		requireSecretStoreError(t, err, private)
	}
	for _, sentinels := range []map[string]api.Secret{
		{"STORE_KEY": ""}, {"STORE_KEY": api.Secret(private + "\x00")},
		{"STORE_KEY": "sentinel", "BAD-NAME": api.Secret(private)},
	} {
		_, err := secretStoreEnvironment(nil, sentinels, credentials, "STORE_KEY")
		requireSecretStoreError(t, err, private)
	}
}

func TestSecretStoreDiscardSharesOneBoundAndCancelsWithoutRetainingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &secretStoreDiscard{remaining: maxSecretStoreOutput, cancel: cancel}
	if _, err := io.Copy(output, strings.NewReader(strings.Repeat("a", maxSecretStoreOutput-1))); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("b")); err != nil || ctx.Err() != nil || output.exceeded() {
		t.Fatal("the exact shared output budget was rejected")
	}
	if _, err := output.Write([]byte("private-output-marker")); !errors.Is(err, errSecretStoreOutput) || ctx.Err() == nil || !output.exceeded() || strings.Contains(err.Error(), "private-output-marker") {
		t.Fatal("output overflow did not cancel with a safe bounded error")
	}
	if count, err := output.Write([]byte("more")); count != 0 || err == nil {
		t.Fatal("output resumed after its batch budget was exhausted")
	}
}

func TestSecretStoreDiscardIsSafeForConcurrentStdoutAndStderr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &secretStoreDiscard{remaining: maxSecretStoreOutput, cancel: cancel}
	var writers sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, err := output.Write([]byte(strings.Repeat("x", maxSecretStoreOutput/8)))
			failures <- err
		}()
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil || output.exceeded() {
		t.Fatal("concurrent output lost the exact shared budget")
	}
	if _, err := output.Write([]byte("x")); err == nil || ctx.Err() == nil {
		t.Fatal("stdout and stderr obtained separate budgets")
	}
}

func TestSecretStoreErrorsKeepExposureAlternativeQualified(t *testing.T) {
	err := secretStoreError("STORE_KEY")
	requireSecretStoreError(t, err)
	failure := err.(*proto.Error)
	if !strings.Contains(failure.Hint, "--expose STORE_KEY") || !strings.Contains(failure.Hint, "only if") || !strings.Contains(failure.Hint, "stored login may still be required") {
		t.Fatal("Store failure promised an environment-only login works for every tool")
	}
	requireSecretStoreError(t, secretStoreError("bad-name-private"), "bad-name-private")
}

func TestSecretStorePlansRedactEveryFormattingVerb(t *testing.T) {
	const marker = "private-store-plan-marker"
	plan := secretStorePlan{name: "KEY", argv: []string{marker}, sentinel: marker}
	for _, value := range []any{plan, &plan} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if text := fmt.Sprintf(verb, value); text != api.Redacted || strings.Contains(text, marker) {
				t.Fatal("Store plan formatting exposed credentials or command details")
			}
		}
	}
}

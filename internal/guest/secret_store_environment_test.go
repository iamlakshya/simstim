package guest

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
)

// These direct arrays are SOURCE witnesses for fields emitted by frozen
// walled_env/members/ops. They are explicitly not a claim that this test called
// an upstream host constructor or started a wall. The actual guest call oracle
// separately consumes these differing metadata fields through real Store.
func directStoreMemberEnvWitness(member bool) []string {
	id, hook, term, color, program, version := "parent234", host.GuestHookSocket(), "xterm-256color", "truecolor", "terminal-one", "1"
	if member {
		id, hook, term, color, program, version = "child234", host.GuestRunDir()+"/hook-child234.sock", "screen-256color", "24bit", "terminal-two", "2"
	}
	return []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin", "XDG_CONFIG_HOME=/home/agent/.config", "HTTP_PROXY=http://127.0.0.1:3128", "SSL_CERT_FILE=" + host.GuestRunDir() + "/ca-bundle.pem", "TOOL_HOME=/home/agent/.tool", "USER_SETTING=kept", brand.Env("SESSION") + "=" + id, brand.Env("HOOK_SOCK") + "=" + hook, "TERM=" + term, "COLORTERM=" + color, "TERM_PROGRAM=" + program, "TERM_PROGRAM_VERSION=" + version}
}

func TestSecretStoreDirectMemberMetadataWitnessHasSameProjectedInvocation(t *testing.T) {
	primary, member := directStoreMemberEnvWitness(false), directStoreMemberEnvWitness(true)
	originalPrimary, originalMember := slices.Clone(primary), slices.Clone(member)
	if slices.Equal(primary, member) {
		t.Fatal("source witness failed to include per-member metadata differences")
	}
	public := map[string]api.Secret{"KEY": "public-stdin-only", "OTHER": "other-public"}
	credentials := map[string]bool{"KEY": true, "OTHER": true, "EXPOSED": true}
	first, err := secretStoreEnvironment(primary, public, credentials, "KEY")
	if err != nil {
		t.Fatal(err)
	}
	second, err := secretStoreEnvironment(member, public, credentials, "KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) || !slices.Equal(primary, originalPrimary) || !slices.Equal(member, originalMember) {
		t.Fatal("metadata-only differences changed Store invocation or mutated agent Env")
	}
	want := primary[:7]
	if !reflect.DeepEqual(first, want) {
		t.Fatal("Store dropped meaningful configuration or retained app/terminal transport metadata")
	}
	for _, entry := range first {
		name, _, _ := strings.Cut(entry, "=")
		if secretStoreMetadata(name) || name == "KEY" || name == "OTHER" || name == "EXPOSED" {
			t.Fatal("Store environment gained metadata or a secret addition")
		}
	}
	owner := newSecretStoreOwnership()
	g := &Init{}
	plan := secretStorePlan{name: "KEY", argv: []string{"tool", "login"}, sentinel: "public-stdin-only"}
	calls := 0
	if err := owner.install(context.Background(), "KEY", g.secretStoreInvocation(plan, "/work", first), func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := owner.install(context.Background(), "KEY", g.secretStoreInvocation(plan, "/work", second), func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatal("same meaningful invocation did not skip the rewrite")
	}
}

func TestSecretStorePreservesEveryMeaningfulContextAndUnknownAppKey(t *testing.T) {
	base := directStoreMemberEnvWitness(false)
	base = append(base, brand.Env("USER_TOOL_SETTING")+"=preserved", "COLUMNS=120", "LINES=40", "NO_COLOR=1")
	projected, err := secretStoreEnvironment(base, nil, nil, "KEY")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if !secretStoreMetadata(name) && !slices.Contains(projected, entry) {
			t.Fatal("projection used an app-prefix or terminal-setting heuristic")
		}
	}
	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "HTTP_PROXY", "SSL_CERT_FILE", "TOOL_HOME", "USER_SETTING", brand.Env("USER_TOOL_SETTING"), "COLUMNS", "LINES", "NO_COLOR"} {
		t.Run(key, func(t *testing.T) {
			changed := slices.Clone(base)
			for i, entry := range changed {
				if strings.HasPrefix(entry, key+"=") {
					changed[i] = key + "=changed-meaningful-value"
				}
			}
			other, err := secretStoreEnvironment(changed, nil, nil, "KEY")
			if err != nil {
				t.Fatal(err)
			}
			owner := newSecretStoreOwnership()
			calls := 0
			g := &Init{}
			plan := secretStorePlan{argv: []string{"tool", "login"}, sentinel: "public"}
			if err := owner.install(context.Background(), "KEY", g.secretStoreInvocation(plan, "/work", projected), func() error { calls++; return nil }); err != nil {
				t.Fatal(err)
			}
			err = owner.install(context.Background(), "KEY", g.secretStoreInvocation(plan, "/work", other), func() error { calls++; return nil })
			requireSecretStoreError(t, err, "changed-meaningful-value")
			if calls != 1 {
				t.Fatal("changed meaningful Store context was ignored")
			}
		})
	}
}

func TestSecretStoreValidatesDroppedMetadataBeforeFiltering(t *testing.T) {
	for _, base := range [][]string{
		{"TERM=one", "TERM=two"},
		{brand.Env("SESSION") + "=first", brand.Env("SESSION") + "=second"},
		{"TERM=bad\x00value"},
		{"TERM"},
	} {
		_, err := secretStoreEnvironment(base, nil, nil, "KEY")
		requireSecretStoreError(t, err, "bad\x00value")
	}
	_, err := secretStoreEnvironment([]string{"TERM=value"}, map[string]api.Secret{"TERM": "public"}, nil, "TERM")
	requireSecretStoreError(t, err)
}

func TestSecretStoreClaimUsesActualOwnedPathMappingOnly(t *testing.T) {
	g := &Init{root: "/tmp/owned-root"}
	plan := secretStorePlan{argv: []string{"tool", "login"}, sentinel: "public"}
	env := []string{"HOME=/home/agent", "SSL_CERT_FILE=" + host.GuestRunDir() + "/ca.pem", "USER_SETTING=kept"}
	claim := g.secretStoreInvocation(plan, "/home/agent/work", env)
	if !reflect.DeepEqual(claim.env, g.mapEnv(env)) || claim.cwd != g.mapValue("/home/agent/work") || !slices.Equal(claim.argv, plan.argv) {
		t.Fatal("claim compared a different environment/directory than the owned command")
	}
	if strings.Contains(fmt.Sprintf("%#v", claim), "USER_SETTING") {
		t.Fatal("actual context mapping leaked from formatter")
	}
}

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func machineValueHostFixtureV1(t *testing.T) Host {
	t.Helper()
	before := h5GrantState(t)
	after := cloneObservationHost(before)
	after.Machine = &Machine{Provider: "e2b", ID: "named-source-machine", Session: "work"}
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host set", Was: &before, Now: &after, Tier: TierOwn, HostID: strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}
	saved, err := FindHost(after.Name)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func machineNamedValueFixtureV1(t *testing.T, minimum string) (context.Context, *newSecretPlan, NewArgs, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the named-key marker uses a POSIX shell")
	}
	host := machineValueHostFixtureV1(t)
	marker := filepath.Join(t.TempDir(), "named-value-command")
	command := "printf synthetic-machine-key; printf x >> " + ShellQuote(marker)
	config := fmt.Sprintf("[secrets.KEY]\nrun=%q\nkind='ssh-key'\non_hosts=['box']\n", command)
	if minimum != "" {
		config += "[secret_tiers]\nKEY=" + fmt.Sprintf("%q", minimum) + "\n"
	}
	writeRetainedImagePolicyFixture(t, config)
	conn := finishGeneratedTransportFixture(t, host, strings.Repeat("a", 32), "session.id", api.FeatureNamedSSHKeysV1, api.FeatureBoundSecrets)
	request := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), request)
	t.Cleanup(func() {
		if err := request.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := request.BindNewWall(ctx, "process", nil); err != nil {
		t.Fatal(err)
	}
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args := NewArgs{Wall: "process", Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"KEY"}}}}}}
	plan := &newSecretPlan{conn: conn, operation: operation, selection: &LocalAgentSelection{host: host, kind: "shell"}, layers: api.CloneLayers(args.Layers)}
	return ctx, plan, args, marker
}

func TestMachineNamedKeyRaisedTierStillRequiresSharedMarkAndOwnAnswer(t *testing.T) {
	for _, minimum := range []string{"", "own", "throwaway", "shared"} {
		t.Run(minimum, func(t *testing.T) {
			ctx, plan, args, marker := machineNamedValueFixtureV1(t, minimum)
			_, err := prepareNewNamedSSHKeys(ctx, plan, args)
			if _, markerErr := os.Stat(marker); !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatal("named value ran before machine preflight")
			}
			if minimum != "shared" {
				var userErr *UserError
				if !errors.As(err, &userErr) || userErr.Code != "tier_refused" {
					t.Fatalf("machine minimum %q: %v", minimum, err)
				}
				return
			}
			var question *ConfirmError
			if !errors.As(err, &question) || question.Grant.Kind != OutboundSSHKey {
				t.Fatalf("raised tier bypassed host-owner consent: %v", err)
			}
			ctx, err = ConfirmGrant(ctx, err)
			if err != nil {
				t.Fatal(err)
			}
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
			count, err := os.ReadFile(marker)
			if err != nil || string(count) != "x" {
				t.Fatalf("named value reads=%q, %v", count, err)
			}
			if len(plan.values) != 0 {
				t.Fatal("named private key entered the ordinary guest value map")
			}
		})
	}
}

func TestMachineNamedKeyCancelledAnswerNeverFetches(t *testing.T) {
	ctx, plan, args, marker := machineNamedValueFixtureV1(t, "shared")
	_, question := prepareNewNamedSSHKeys(ctx, plan, args)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ConfirmGrant(cancelled, question); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled answer=%v", err)
	}
	_, repeated := prepareNewNamedSSHKeys(ctx, plan, args)
	var stillPending *ConfirmError
	if !errors.As(repeated, &stillPending) {
		t.Fatal("cancelled child consent marked the still-live parent request approved")
	}
	if err := grantRequestOf(ctx).namedNew.fetch(cancelled, plan, false); err == nil {
		t.Fatal("cancelled named consent fetched a value")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled named consent ran its command")
	}
}

func TestMachineNamedKeyAnswerCannotMoveToAnotherConnectionOrReadd(t *testing.T) {
	ctx, plan, args, marker := machineNamedValueFixtureV1(t, "shared")
	_, question := prepareNewNamedSSHKeys(ctx, plan, args)
	ctx, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := prepareNewNamedSSHKeys(ctx, plan, args)
	if err != nil {
		t.Fatal(err)
	}
	other := finishGeneratedTransportFixture(t, plan.conn.Host, plan.conn.HostID(), "session.id", api.FeatureNamedSSHKeysV1, api.FeatureBoundSecrets)
	borrowed := &newSecretPlan{conn: other, operation: plan.operation, selection: plan.selection}
	if err := keys.fetch(ctx, borrowed, false); err == nil {
		t.Fatal("another connection borrowed the original named-key answer")
	}
	host := plan.conn.Host
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host rm", Was: &host}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host add", Now: &host, Tier: TierOwn, HostID: plan.conn.HostID()}); err != nil {
		t.Fatal(err)
	}
	if err := keys.fetch(ctx, plan, false); err == nil {
		t.Fatal("same-row re-add reused the old named-key answer")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale named-key permission ran a value command")
	}
}

func TestMachineHostExposureLedgerDataCannotSupplyNamedConsent(t *testing.T) {
	ctx, plan, args, marker := machineNamedValueFixtureV1(t, "shared")
	when := nowUTC()
	data, err := encodeGrantsFile(grantsFile{Version: 1, Grants: []GrantRecord{{HostName: plan.conn.Host.Name, HostID: plan.conn.HostID(), Kind: OutboundSSHKey, Name: "KEY", Exposure: "host", FirstAt: when, LastAt: when, Count: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(StateDir(), grantsFileName), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = prepareNewNamedSSHKeys(ctx, plan, args)
	var question *ConfirmError
	if !errors.As(err, &question) {
		t.Fatalf("exposure history manufactured consent: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exposure history ran a named value command")
	}
	decoded, err := decodeGrantsFile(data)
	if err != nil || decoded.Grants[0].Exposure != "host" {
		t.Fatalf("exposure history lost metadata: %v", err)
	}
	view, err := json.Marshal(decoded.Grants[0])
	if err != nil || !strings.Contains(string(view), `"exposure":"host"`) {
		t.Fatal("host exposure was not visible as data")
	}
	if details := SentDetails(plan.conn.Host, plan.conn.HostID(), decoded.Grants); !strings.Contains(fmt.Sprint(details), "exposed to host") {
		t.Fatal("host exposure was hidden in the sent view")
	}
}

func TestMachineNamedPhysicalSendRecordsHostExposureOnly(t *testing.T) {
	ctx, plan, args, _ := machineNamedValueFixtureV1(t, "shared")
	_, question := prepareNewNamedSSHKeys(ctx, plan, args)
	ctx, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := prepareNewNamedSSHKeys(ctx, plan, args)
	if err != nil {
		t.Fatal(err)
	}
	plan.namedKeys = keys
	if err := keys.fetch(ctx, plan, false); err != nil {
		t.Fatal(err)
	}
	carrier, err := keys.arguments(nil)
	if err != nil {
		t.Fatal(err)
	}
	owned, release, err := plan.conn.beginGrant(ctx, "original named-key physical fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	id, token, err := grantRequestOf(ctx).identity()
	if err != nil {
		t.Fatal(err)
	}
	creation := SandboxPolicy{SessionID: id, HostID: plan.conn.HostID(), Ceiling: TierOwn, CreationWall: "process", CreatedAt: nowUTC(), request: token}
	if err := reserveSandbox(owned, plan.conn.Host, creation); err != nil {
		t.Fatal(err)
	}
	plan.creation, err = LoadSandboxPolicy(owned, plan.conn.Host, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.beforePhysical(owned, plan, carrier); err != nil {
		t.Fatal(err)
	}
	release()
	records, err := GrantsForHostContext(t.Context(), plan.conn.Host, plan.conn.HostID())
	if err != nil || len(records) != 1 || records[0].Kind != OutboundSSHKey || records[0].Exposure != "host" {
		t.Fatalf("physical named exposure=%v, %v", records, err)
	}
	encoded, err := os.ReadFile(filepath.Join(StateDir(), grantsFileName))
	if err != nil || strings.Contains(string(encoded), "synthetic-machine-key") {
		t.Fatal("exposure history stored private key values")
	}
	if len(plan.values) != 0 || len(args.Secrets) != 0 {
		t.Fatal("host-only signer values entered the ordinary guest environment")
	}
}

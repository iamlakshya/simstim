package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func networkHoldNativeConfigFixtureV1(t *testing.T, initial string, durations ...time.Duration) (*networkHoldOperation, context.Context, []byte) {
	t.Helper()
	host := h5GrantState(t)
	if initial != "" {
		if err := os.WriteFile(settingsPath(), []byte(initial), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := api.NetworkHoldActionArgs{Version: 1, Session: "hold2345", Request: strings.Repeat("c", 32), Scope: api.NetworkHoldProfile}
	selected := FileLayer{Kind: api.LayerProfile, Name: "project"}
	request, err := NewNetworkHoldActionRequestV1(args, selected)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), request)
	conn := finishGeneratedTransportFixture(t, host, strings.Repeat("a", 32), api.FeatureNetworkHolds, api.FeatureNetworkPublicOnly)
	conn.pending = map[uint64]chan proto.Response{}
	duration := time.Minute
	if len(durations) != 0 {
		duration = durations[0]
	}
	expires := time.Now().Add(duration).UTC()
	sharedGrantWire(t, conn, func(frame proto.Request) any {
		switch frame.Op {
		case api.OpPolicyDescribe:
			return api.NetworkHoldDescriptor{Version: 1, Request: args.Request, Revision: 7, Class: api.NetworkHoldPublicTarget, Expires: expires}
		case api.OpPolicyResolve:
			return api.NetworkHoldResolution{Version: 1, Request: args.Request, Revision: 7, Class: api.NetworkHoldPublicTarget, Expires: expires, Scope: args.Scope, Target: "api.example.com:443"}
		default:
			t.Fatalf("unexpected native publication request: %s", frame.Op)
			return nil
		}
	})
	operation, err := newNetworkHoldOperation(ctx, conn, args)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := request.Close(); err != nil {
			t.Error(err)
		}
	})
	return operation, ctx, []byte(initial)
}

func TestHeldPublicExportUsesOriginalNativeConfigAndPreservesOtherTables(t *testing.T) {
	operation, ctx, _ := networkHoldNativeConfigFixtureV1(t, "[agents]\ninline = true\n\n[profiles.project.network]\nallow = [\"prior.example\"]\n")
	result, err := operation.publish(ctx, operation.selected)
	if err != nil || result.Rule.Target != "api.example.com:443" || !result.Rule.PublicOnly || result.Applies != "next_start" {
		t.Fatalf("actual complete export/native file publication failed: %#v %v", result, err)
	}
	raw, err := os.ReadFile(settingsPath())
	if err != nil || !bytes.Contains(raw, []byte("api.example.com:443")) || !bytes.Contains(raw, []byte("prior.example")) || !bytes.Contains(raw, []byte("inline = true")) {
		t.Fatal("native writer lost unrelated original settings", err)
	}
	if _, err := operation.publish(ctx, operation.selected); err == nil {
		t.Fatal("the old settings action renewed itself after its own publication")
	}
}

func TestHeldConfigHashRefusesRestoredMetadataAndUnrelatedPublicDTO(t *testing.T) {
	operation, ctx, original := networkHoldNativeConfigFixtureV1(t, "[agents]\ninline = true \n")
	if _, err := operation.resolve(ctx); err != nil {
		t.Fatal(err)
	}
	// A public value cannot install the private file-publication context.
	if _, err := persistResolvedNetworkHoldV1(ctx, operation); err == nil {
		t.Fatal("an ordinary context became file publication authority")
	}
	before, err := os.Stat(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(original, []byte("true "), []byte("false"), 1)
	if err := os.WriteFile(settingsPath(), changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(settingsPath(), before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := operation.settings.Revalidate(ctx); err != nil {
		t.Fatal("fixture did not preserve the old size/inode/time metadata", err)
	}
	if _, err := operation.publish(ctx, operation.selected); err == nil {
		t.Fatal("restoring file metadata renewed original content authority")
	}
	raw, _ := os.ReadFile(settingsPath())
	if !bytes.Equal(raw, changed) {
		t.Fatal("changed config was overwritten after a stale-source refusal")
	}
}

func TestHeldNumericExportRemainsExactPublicOnlyAtEveryTier(t *testing.T) {
	operation := &networkHoldOperation{maximum: api.NetworkHoldExistingPolicy}
	selected := FileLayer{Kind: api.LayerProfile, Name: "project"}
	for _, target := range []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		rule, err := resolvedNetworkHoldRuleV1(operation, selected, api.NetworkHoldResolution{Target: target, Class: api.NetworkHoldPublicTarget})
		if err != nil || !rule.PublicOnly {
			t.Fatal("own numerical action lost its restrictive bit", target, err)
		}
	}
	for _, target := range []string{"127.0.0.1:443", netip.AddrFrom4([4]byte{10, 0, 0, 1}).String() + ":443", "8.8.8.0/24:443", "*.example.com:443"} {
		if _, err := resolvedNetworkHoldRuleV1(operation, selected, api.NetworkHoldResolution{Target: target, Class: api.NetworkHoldPublicTarget}); err == nil {
			t.Fatal("a private or wider target became an exact public export", target)
		}
	}
	// The operation exposes no serialised native FD, content hash or source.
	raw, err := json.Marshal(&networkHoldConfigV1{digest: [32]byte{1}})
	if err != nil || string(raw) != "{}" {
		t.Fatal("native provenance escaped as public metadata", string(raw), err)
	}
}

func TestHeldNativeFileLockCannotRenewOriginalExportExpiry(t *testing.T) {
	operation, ctx, original := networkHoldNativeConfigFixtureV1(t, "[agents]\ninline = true\n", 1500*time.Millisecond)
	if _, err := operation.resolve(ctx); err != nil {
		t.Fatal(err)
	}
	held, err := acquireStateFileLock(ctx, operation.config.root, "config.toml", true)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = held.closeOwned(ctx) }) }
	t.Cleanup(release)
	operation.mu.Lock()
	expiry := operation.descriptor.Expires
	operation.mu.Unlock()
	timer := time.AfterFunc(time.Until(expiry)+50*time.Millisecond, release)
	defer timer.Stop()
	if _, err := operation.publish(ctx, operation.selected); err == nil {
		t.Fatal("a delayed file lock renewed an expired held-request write")
	}
	release()
	raw, err := os.ReadFile(settingsPath())
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("expired native publication changed the original config", err)
	}
}

func TestHeldNativeSnapshotRechecksCurrentTierAfterAuthorization(t *testing.T) {
	operation, ctx, _ := networkHoldNativeConfigFixtureV1(t, "[agents]\ninline = true\n")
	if _, err := operation.resolve(ctx); err != nil {
		t.Fatal(err)
	}
	operation.mu.Lock()
	tier, maximum := operation.tier, operation.maximum
	operation.mu.Unlock()
	// This negative-only snapshot test does not enter the native writer.
	if err := operation.config.authorizeFile(ctx, tier, maximum); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(ctx, operation.conn.Host.Name, TierShared, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := operation.config.check(ctx); err == nil {
		t.Fatal("a changed tier or membership renewed an older file authorisation")
	}
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if operation.tier > tier || maximum == api.NetworkHoldPublicOnly && operation.maximum != maximum {
		t.Fatal("the boundary check widened the original tier or maximum")
	}
}

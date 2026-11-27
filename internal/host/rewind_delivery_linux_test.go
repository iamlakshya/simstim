//go:build linux

package host

import (
	"fmt"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRewindDeliveryCannotRearmOrdinaryOneUseCarriersFromData(t *testing.T) {
	operation := &SessionOperation{id: "parent23", namedSSHKeysConsumed: true, protectedTrustedConsumed: true}
	cold := &ColdResumeV1{operation: operation}
	p := &bridgeRewindPreparationV1{operation: operation, cold: cold, accounting: &resumePreparationLeaseV1{}, execution: t.Context(), consumed: true, prepareRequest: 1, commitRequest: 2, commitEncoded: []byte("{}")}
	p.active.Store(true)
	p.phase.Store(rewindRestartingV1)
	operation.rewindPreparation = p
	m := &Manifest{ID: operation.id, Policy: &api.Policy{}}
	if err := p.captureStartupDeliveryV1(t.Context(), cold, m, api.ResumeArgs{ID: operation.id}); err == nil {
		t.Fatal("public request data supplied a native delivery source")
	}
	if !operation.namedSSHKeysConsumed || !operation.protectedTrustedConsumed || operation.namedSSHKeys != nil || operation.protectedTrustedStart != nil || p.startupDelivery != nil {
		t.Fatal("failed rewind delivery reset or replaced ordinary one-use custody")
	}
	launch := &startupLaunchV1{operation: operation}
	if _, _, _, err := p.takeStartupDeliveryV1(t.Context(), launch, nil, nil); err == nil {
		t.Fatal("an unproved launch took private rewind values")
	}
}

func TestRewindDeliveryFormattingAndCleanupKeepPrivateValuesPrivate(t *testing.T) {
	secret := api.Secret("private-value")
	previous := &rewindStartupDeliveryV1{primary: protectedTrustedProcessV1{Values: map[string]api.Secret{"TOKEN": secret}}}
	delivery := &rewindStartupDeliveryV1{previous: previous, named: &api.NamedSSHKeysV1{Values: map[string]api.Secret{"KEY": secret}}, primary: protectedTrustedProcessV1{Values: map[string]api.Secret{"TOKEN": secret}}}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, delivery); got != api.Redacted {
			t.Fatalf("private delivery formatting exposed values: %q", got)
		}
	}
	delivery.clearV1()
	delivery.clearV1()
	if !delivery.closed || !previous.closed || len(delivery.named.Values) != 0 || len(delivery.primary.Values) != 0 || len(previous.primary.Values) != 0 {
		t.Fatal("cleanup did not clear every retained private attempt")
	}
}

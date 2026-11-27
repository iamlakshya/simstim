package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHostSourceWitnessUsesActualCanonicalRootAndNeverRawPermission(t *testing.T) {
	h, ctx, observation, _ := hostObservationFixture(t)
	c := observation.checkedConn
	witness, err := captureHostStateSourceWitness(ctx, c)
	if err != nil || !witness.Root.Known() || !witness.Root.Directory || witness.Root.Identity != observation.source.root.Identity ||
		!witness.Root.Modified.Equal(observation.source.root.Modified) || witness.Root.Modified.Location().String() != "UTC" {
		t.Fatalf("actual native root was zeroed, fabricated or lost UTC: %+v, %v", witness, err)
	}
	raw, err := json.Marshal(witness)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeHostStateSourceWitnessContext(t.Context(), raw)
	if err != nil || loaded.validate() != nil || loaded.check(ctx, c) != nil {
		t.Fatalf("same checked actual Source failed round trip: %v", err)
	}
	if err := loaded.check(t.Context(), c); err == nil {
		t.Fatal("raw Source metadata created its own OP/observation authority")
	}
	manual := generatedHostTransportConn(h, c.HostID())
	if err := loaded.check(ctx, manual); err == nil {
		t.Fatal("raw Source plus a manual Conn fabricated checked transport")
	}
}

func TestHostSourceWitnessRejectsNewEpochAndWrongNativeSource(t *testing.T) {
	h, ctx, observation, release := hostObservationFixture(t)
	c := observation.checkedConn
	witness, err := captureHostStateSourceWitness(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	wrong := witness.clone()
	wrong.Root.Identity += ":foreign"
	if err := wrong.check(ctx, c); err == nil {
		t.Fatal("a different native root became original Source authority")
	}
	release()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	freshConn := finishGeneratedTransportFixture(t, h, c.HostID())
	fresh, _, _ := transportPublicationFixture(t, h, c.HostID())
	if err := witness.check(fresh, freshConn); err == nil {
		t.Fatal("fresh Conn/OP relabelled historical Source after identical re-add")
	}
}

func TestHostSourceWitnessFutureAliasesDuplicatesAndUnknownNativeNeverMatch(t *testing.T) {
	_, ctx, observation, _ := hostObservationFixture(t)
	witness, err := captureHostStateSourceWitness(ctx, observation.checkedConn)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(witness)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"alias", bytes.Replace(raw, []byte(`"generation"`), []byte(`"Generation"`), 1)},
		{"duplicate", bytes.Replace(raw, []byte(`"exists":true`), []byte(`"exists":true,"exists":true`), 1)},
		{"root-duplicate", bytes.Replace(raw, []byte(`"directory":true`), []byte(`"directory":true,"directory":true`), 1)},
		{"root-alias", bytes.Replace(raw, []byte(`"identity"`), []byte(`"Identity"`), 1)},
		{"unknown", append([]byte(`{"future": { "n":1.00 },`), raw[1:]...)},
		{"native-unknown", bytes.Replace(raw, []byte(`"directory":true`), []byte(`"future": { "n":1.00 },"directory":true`), 1)},
		{"absent", bytes.Replace(raw, []byte(`"exists":true`), []byte(`"exists":false`), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Equal(tc.raw, raw) {
				t.Fatal("source fixture missed its target")
			}
			loaded, err := decodeHostStateSourceWitnessContext(t.Context(), tc.raw)
			if err == nil && loaded.validate() == nil {
				t.Fatal("aliased/duplicate/future/absent Source metadata became known authority")
			}
		})
	}
	future := []byte(`{ "version":2, "future_native": { "keep":"\u0061", "n":1.00 } }`)
	loaded, err := decodeHostStateSourceWitnessContext(t.Context(), future)
	if err != nil || loaded.validate() == nil {
		t.Fatalf("unsupported Source wrapper was discarded or promoted: %v", err)
	}
	clone := loaded.clone()
	clone.future[0] = '['
	preserved, err := loaded.MarshalJSON()
	if err != nil || !bytes.Equal(preserved, future) {
		t.Fatalf("future Source raw bytes changed during clone: %v", err)
	}
}

func TestHostSourceWitnessRejectsUncheckedClassAndPreservesDecodeCancellation(t *testing.T) {
	_, ctx, _, c, _ := legacyStateTransportFixture(t)
	if _, err := captureHostStateSourceWitness(ctx, c); err == nil {
		t.Fatal("unchecked legacy connection minted modern receipt Source authority")
	}
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := decodeHostStateSourceWitnessContext(stopped, []byte(`{"version":1}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Source byte adapter lost cancellation: %v", err)
	}
	for _, source := range []hostStateSourceWitness{{}, {Version: 1, Exists: true, Generation: strings.Repeat("a", 32), Path: "generated"}} {
		if source.validate() == nil {
			t.Fatal("a constructor ID/path supplied a fabricated native root")
		}
	}
}

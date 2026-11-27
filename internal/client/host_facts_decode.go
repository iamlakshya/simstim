package client

import (
	"bytes"
	"context"
	"encoding/json"
	"math"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func hostFactsProtocolError() error {
	return &UserError{Msg: "the host returned inconsistent or ambiguous capacity facts",
		Hint: "Update the helper and inspect host doctor before choosing this host.", Code: "facts_protocol", Kind: KindRefused}
}

func hostFactsFutureError() error {
	return &UserError{Msg: "the host facts use a format this client cannot select from",
		Hint: "Update this client before using automatic placement on that host.", Code: "facts_future", Kind: KindRefused}
}

func decodeHostFacts(raw []byte, hostID string) (api.HostFacts, error) {
	return decodeHostFactsContext(context.Background(), raw, hostID)
}

func decodeHostFactsContext(ctx context.Context, raw []byte, hostID string) (decoded api.HostFacts, failure error) {
	if ctx == nil {
		return api.HostFacts{}, hostFactsProtocolError()
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			decoded, failure = api.HostFacts{}, err
		}
	}()
	if err := ctx.Err(); err != nil {
		return api.HostFacts{}, err
	}
	var facts api.HostFacts
	version, err := api.ProjectedJSONObjectFields(ctx, raw, "v")
	if err != nil || json.Unmarshal(version["v"], &facts.Version) != nil {
		return facts, hostFactsProtocolError()
	}
	// Other fields of an unknown version stay opaque and unselected.
	if facts.Version != api.HostFactsVersion {
		return api.HostFacts{}, hostFactsFutureError()
	}
	names := api.ExactJSONFieldNames(facts)
	fields, err := api.ProjectedJSONObjectFields(ctx, raw, names...)
	if err != nil {
		return api.HostFacts{}, hostFactsProtocolError()
	}
	futureSlots := false
	if slot := fields["slot_capacity"]; len(slot) != 0 && !bytes.Equal(bytes.TrimSpace(slot), []byte("null")) {
		version, err := api.JSONObjectFields(slot, "v")
		var number int
		if err != nil || json.Unmarshal(version["v"], &number) != nil || number < 1 {
			return api.HostFacts{}, hostFactsProtocolError()
		}
		futureSlots = number > 1
	}
	known := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		if name == "memory_observation" {
			continue // Optional overlap data is decoded separately and cannot hide other known facts.
		}
		if name == "slot_capacity" && futureSlots {
			continue // Future slot DATA stays opaque while known facts validate.
		}
		if value, found := fields[name]; found {
			known[name] = value
		}
	}
	encoded, err := json.Marshal(known)
	if err != nil || json.Unmarshal(encoded, &facts) != nil || !api.ExactJSONFields(encoded, facts) {
		return api.HostFacts{}, hostFactsProtocolError()
	}
	if err := validateHostFacts(facts, hostID); err != nil {
		return api.HostFacts{}, err
	}
	// Recognised v1 always completes its known validation before an opaque
	// extension is classified. No future marker can hide a known failure.
	if sample, exists := fields["memory_observation"]; exists {
		facts.MemoryObservation, err = decodeHostMemoryObservationV1(ctx, sample, &facts)
		if err != nil {
			return api.HostFacts{}, err
		}
	}
	count := len(known)
	if _, exists := fields["memory_observation"]; exists {
		count++
	}
	if len(fields) != count {
		return api.HostFacts{}, hostFactsFutureError()
	}
	return facts, nil
}

func validateHostFacts(f api.HostFacts, hostID string) error {
	clean := func(text string) bool { return CleanLine(text) == text }
	if f.Version != api.HostFactsVersion || !ValidHostID(hostID) || f.HostID != hostID || f.BootID == "" || !clean(f.BootID) ||
		f.At.IsZero() || f.OS == "" || !clean(f.OS) || f.Arch == "" || !clean(f.Arch) || f.MemTotal < 0 || f.MemForSessions < 0 || f.MemReserved < 0 || f.Sandboxes < 0 || f.DiskFree < 0 {
		return hostFactsProtocolError()
	}
	if f.MemoryKnown && (f.MemTotal <= 0 || f.MemForSessions <= 0 || f.MemForSessions > f.MemTotal) ||
		f.AccountingKnown && (!f.MemoryKnown || f.Room != f.MemForSessions-f.MemReserved) {
		return hostFactsProtocolError()
	}
	if f.LevelKnown && wallLevelRank(f.Level) == 0 || !clean(f.Level) {
		return hostFactsProtocolError()
	}
	for _, pressure := range []float64{f.PressureMem, f.PressureCPU} {
		if math.IsNaN(pressure) || math.IsInf(pressure, 0) || pressure < 0 || pressure > 100 {
			return hostFactsProtocolError()
		}
	}
	for _, probe := range f.Walls {
		if wallLevelRank(probe.Level) == 0 || probe.Driver == "" || !clean(probe.Driver) || !clean(probe.Reason) || !clean(probe.Fix) {
			return hostFactsProtocolError()
		}
		// Version and free-form probe facts remain opaque identity/text.
		// Display uses CleanWallProbes; neither field grants readiness.
	}
	for _, digest := range f.RuntimeDigests {
		if !supply.IsHex64(digest) {
			return hostFactsProtocolError()
		}
	}
	for _, digest := range f.ImageDigests {
		if !image.ValidDigest(digest) {
			return hostFactsProtocolError()
		}
	}
	for _, health := range f.Health {
		if health.ID == "" || !clean(health.ID) || !clean(health.Message) || !clean(health.Hint) ||
			health.Severity != "ok" && health.Severity != "warn" && health.Severity != "blocks_new" {
			return hostFactsProtocolError()
		}
		for _, level := range health.Levels {
			if wallLevelRank(level) == 0 {
				return hostFactsProtocolError()
			}
		}
	}
	if err := api.ValidateSlotCapacityV1(f.SlotCapacity); err != nil {
		return hostFactsProtocolError()
	}
	return nil
}

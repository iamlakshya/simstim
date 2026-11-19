package client

import (
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
	var facts api.HostFacts
	version, err := api.JSONObjectFields(raw, "v")
	if err != nil || json.Unmarshal(version["v"], &facts.Version) != nil {
		return facts, hostFactsProtocolError()
	}
	// Other fields of an unknown version stay opaque and unselected.
	if facts.Version != api.HostFactsVersion {
		return api.HostFacts{}, hostFactsFutureError()
	}
	names := api.ExactJSONFieldNames(facts)
	fields, err := api.JSONObjectFields(raw, names...)
	if err != nil || json.Unmarshal(raw, &facts) != nil {
		return api.HostFacts{}, hostFactsProtocolError()
	}
	known := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		if value, found := fields[name]; found {
			known[name] = value
		}
	}
	encoded, err := json.Marshal(known)
	if err != nil || !api.ExactJSONFields(encoded, facts) {
		return api.HostFacts{}, hostFactsProtocolError()
	}
	if err := validateHostFacts(facts, hostID); err != nil {
		return api.HostFacts{}, err
	}
	// Recognised v1 always completes its known validation before an opaque
	// extension is classified. No future marker can hide a known failure.
	if len(fields) != len(known) {
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
	return nil
}

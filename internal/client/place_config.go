package client

import (
	"strings"
	"time"
	"unicode"
)

// HostPlacement contains only local selection metadata. Features are user
// labels, not capabilities the host may grant itself through a facts reply.
type HostPlacement struct {
	// Pool explicitly includes or excludes the host from automatic placement.
	Pool *bool `toml:"pool,omitempty"`
	// Features lists the user labels available for --need.
	Features []string `toml:"features,omitempty"`
	// Mandatory lists labels every request must name to use this host.
	Mandatory []string `toml:"mandatory,omitempty"`
	// Cost is free or paid for a wake. Empty uses the provider's cost.
	Cost string `toml:"cost,omitempty"`
	// MaxSandboxes is a local limit on all clients' sandboxes. Zero has no limit.
	MaxSandboxes int `toml:"max_sandboxes,omitempty"`
}

// PreparedHost is a cache of actual preparation observations. Later live
// facts may only agree with it or make a host ineligible; it grants nothing.
type PreparedHost struct {
	Level          string    `toml:"level"`
	LevelAfterRoot string    `toml:"level_after_root,omitempty"`
	Runtime        string    `toml:"runtime,omitempty"`
	Arch           string    `toml:"arch"`
	MemTotal       int64     `toml:"mem_total"`
	PreparedAt     time.Time `toml:"prepared_at"`
}

// PlacementSettings controls deterministic ranking after the wake class.
type PlacementSettings struct {
	// Strategy is spread or pack: most room, or least room that fits.
	Strategy string `toml:"strategy"`
	// Prefer ranks named hosts first within their wake class and penalty.
	Prefer []string `toml:"prefer,omitempty"`
	// Wake is ask, always or never. Paid wakes ask unless explicitly allowed.
	Wake string `toml:"wake"`
}

func placementConfigError() error {
	return &UserError{Msg: "automatic placement has an invalid local configuration",
		Hint: "Check placement strategy/wake and host.place feature, cost and max_sandboxes values.", Code: "bad_placement", Kind: KindUsage}
}

func validatePlacementSettings(settings PlacementSettings) error {
	if settings.Strategy != "" && settings.Strategy != "spread" && settings.Strategy != "pack" ||
		settings.Wake != "" && settings.Wake != "ask" && settings.Wake != "always" && settings.Wake != "never" {
		return placementConfigError()
	}
	for _, name := range settings.Prefer {
		if !ValidHostName(name) {
			return placementConfigError()
		}
	}
	return nil
}

func validateHostPlacement(host Host) error {
	if host.Place.MaxSandboxes < 0 || !validPlacementCost(host.Place.Cost) || host.Provider != nil && !validPlacementCost(host.Provider.Cost) {
		return placementConfigError()
	}
	for _, features := range [][]string{host.Place.Features, host.Place.Mandatory} {
		for _, feature := range features {
			if !validPlacementFeature(feature) {
				return placementConfigError()
			}
		}
	}
	return nil
}

func validPlacementCost(cost string) bool { return cost == "" || cost == "free" || cost == "paid" }

func validPlacementFeature(feature string) bool {
	return feature != "" && !strings.ContainsAny(feature, ",=") && strings.IndexFunc(feature, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

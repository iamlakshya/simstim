package host

import "github.com/iamlakshya/simstim/internal/api"

func preparePlacementAuditNewV1(args NewArgs) (NewArgs, error) {
	if args.Placement == nil {
		return args, nil
	}
	// Own the request data before repository preparation or any session effect.
	selected := api.ClonePlacementV1(args.Placement)
	if err := selected.Validate(); err != nil {
		return args, err
	}
	args.Placement = selected
	return args, nil
}

func applyPlacementAuditNewV1(m *Manifest, selected *api.PlacementV1) error {
	if m == nil || m.SandboxOf != "" {
		return UserError("bad_placement", "placement descriptions belong only to a new primary session", "Start the primary through its original host-selection request.")
	}
	if selected == nil {
		m.PlacedBy, m.PlaceReason = "", ""
		return nil
	}
	if err := selected.Validate(); err != nil {
		return err
	}
	// This records what the original outside request said. Even a custom SDK
	// label is only metadata; it grants no operation, Need, room or native boot.
	m.PlacedBy, m.PlaceReason = selected.PlacedBy, selected.Reason
	return nil
}

func placementAuditInfoV1(m *Manifest) (string, string) {
	if m == nil || m.SandboxOf != "" {
		return "", ""
	}
	return api.CleanText(m.PlacedBy, 256), api.CleanText(m.PlaceReason, 256)
}

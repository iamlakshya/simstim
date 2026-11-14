package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/gitsource"
)

// GitSourceCapability is the canonical opaque read-only Git source capability.
type GitSourceCapability = gitsource.Capability

// OpenGitSourceCapability uses the SAME full host manifest/private inventory.
func OpenGitSourceCapability(ctx context.Context, name string, graph bool) (*GitSourceCapability, error) {
	return gitsource.Open(ctx, name, graph, hostSourceInventory)
}

// OpenGitSourceCapabilityRestricted adds only actual inspected provider refusals.
func OpenGitSourceCapabilityRestricted(ctx context.Context, name string, graph bool, extra ...GitSourceRestriction) (*GitSourceCapability, error) {
	restrictions := make([]gitsource.Restriction, 0, len(extra))
	for _, r := range extra {
		restrictions = append(restrictions, gitsource.Restriction{Path: r.Path, Identity: r.Identity, Direct: true})
	}
	return gitsource.Open(ctx, name, graph, hostSourceInventory, restrictions...)
}

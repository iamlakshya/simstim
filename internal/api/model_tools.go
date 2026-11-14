package api

import (
	"fmt"
	"slices"
)

// FeatureModelTools prevents an older helper from silently widening a model
// capability grant into an ordinary host allowance.
const FeatureModelTools = "policy-model-tools-v1"

var modelToolNames = []string{"code_execution", "files", "mcp", "url_content", "web_fetch", "web_search"}

// ModelToolNames returns an owned list for capability completion and display.
// Admission uses this same finite table.
func ModelToolNames() []string { return slices.Clone(modelToolNames) }

// CanonicalModelTools owns, sorts and validates a model capability set.
// Empty sets retain the legacy rule identity and carry no extra authority.
func CanonicalModelTools(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	output := slices.Clone(input)
	for _, tool := range output {
		if !slices.Contains(modelToolNames, tool) {
			return nil, fmt.Errorf("unknown model capability %q", tool)
		}
	}
	slices.Sort(output)
	return slices.Compact(output), nil
}

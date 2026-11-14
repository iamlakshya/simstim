package api

// FeatureSandboxGroupRM binds destructive deletion to the listed members.
const FeatureSandboxGroupRM = "sandbox-group-rm-v1"

// DeleteArgs preserves the expected name and optional complete child snapshot.
// A nonnil empty list means that no children were shown, including after JSON.
type DeleteArgs struct {
	ID      string    `json:"id"`
	Name    *string   `json:"name,omitempty"`
	Members *[]string `json:"members,omitempty"`
}

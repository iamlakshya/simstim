package api

// FeatureAgentsDiskV1 guards the actual host-installed-agent producer. Its
// authenticated receipt is an explicit outside-G11 source, not runtime supply.
const FeatureAgentsDiskV1 = "agents-disk-v1"

// AgentsDiskRequestV1 selects no host path, environment, binary, FD or owner.
// The server uses its canonical installed-agent table and current host scope.
type AgentsDiskRequestV1 struct {
	Version int    `json:"version"`
	Arch    string `json:"arch"`
}

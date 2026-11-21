package classify

// Metadata is a private start-time copy from the original protected manifest
// and seed. It is data only. Egress source-checks every string before model IO.
type Metadata struct {
	RepositoryRemote string   `json:"repository_remote,omitempty"`
	Ecosystems       []string `json:"ecosystems,omitempty"`
	Agent            string   `json:"agent,omitempty"`
	Profile          string   `json:"profile,omitempty"`
	Task             string   `json:"task,omitempty"`
}

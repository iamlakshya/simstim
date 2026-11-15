package proto

// Guest DNS modes select only the existing real/synthetic DNS engines.
const (
	DNSReal      = "real"
	DNSSynthetic = "synthetic"
)

// CtlSeed stages one original protected repository bundle before any member starts.
const CtlSeed = "seed"

// EventStateReady is a current-run capture claim, never native cleanup proof.
const EventStateReady = "state_ready"

// SeedFile declares one explicitly included regular file after the bundle.
type SeedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// CtlSeedArgs is the existing supervisor/guest seed transaction. Apply joins
// exactly the matching Token after the original stream's ordered half-close.
type CtlSeedArgs struct {
	Token        string     `json:"token"`
	Repo         string     `json:"repo"`
	Commit       string     `json:"commit"`
	Branch       string     `json:"branch"`
	SourceBranch string     `json:"source_branch"`
	Origin       string     `json:"origin"`
	Size         int64      `json:"size"`
	Scratch      bool       `json:"scratch,omitempty"`
	Apply        bool       `json:"apply,omitempty"`
	Includes     []SeedFile `json:"includes,omitempty"`
}

// CtlSeedResult tells the same sender whether this guest needs its bundle.
type CtlSeedResult struct {
	Needed bool `json:"needed"`
}

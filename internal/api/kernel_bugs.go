package api

// Kernel bug evaluation states reported by host doctor (isolation spec 20.11).
const (
	KernelBugAffected  = "affected"
	KernelBugMaybe     = "maybe"
	KernelBugMitigated = "mitigated"
	KernelBugFixed     = "fixed"
	KernelBugUnknown   = "unknown"
)

// KernelBugResult is one host kernel advisory check result reported by host
// doctor in --json output (isolation spec 20.11). A warning here never lowers
// a host's wall floor or refuses a session start (question 7).
type KernelBugResult struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Status       string `json:"status"`
	Severity     string `json:"severity,omitempty"`
	Detail       string `json:"detail,omitempty"`
	Fix          string `json:"fix,omitempty"`
	URL          string `json:"url,omitempty"`
	FixedVersion string `json:"fixed_version,omitempty"`
}

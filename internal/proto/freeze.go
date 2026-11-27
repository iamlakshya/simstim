package proto

// CtlFreeze and CtlThaw operate only on the original runtime's session
// filesystem. A repeated freeze renews its bounded lease.
const (
	CtlFreeze     = "freeze"
	CtlThaw       = "thaw"
	GuestFreezeV1 = "freeze.v1"
	MaxFreezeMS   = 5000
)

type CtlFreezeArgs struct {
	MaxMS int `json:"max_ms"`
}

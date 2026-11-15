package alerts

// FromGuest labels data reported by the guest. It is never a native fact.
const FromGuest = "guest"

// Native protection and heartbeat event kinds.
const (
	DiskFull   = "disk_full"
	LinkSilent = "link_silent"
)

func init() {
	Kinds = append(Kinds, DiskFull, LinkSilent)
	DefaultEvents = append(DefaultEvents, DiskFull, LinkSilent)
}

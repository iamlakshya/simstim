package alerts

// EgressBlocked is a trusted refusal from the sandbox's network guard.
const EgressBlocked = "egress_blocked"

func init() {
	Kinds = append(Kinds, EgressBlocked)
	DefaultEvents = append(DefaultEvents, EgressBlocked)
}

package alerts

// EgressBlocked is a trusted refusal from the sandbox's network guard.
const EgressBlocked = "egress_blocked"

// EgressUnknown is one accepted unknown-site allowance in the live actor.
const EgressUnknown = "egress_unknown"

func init() {
	Kinds = append(Kinds, EgressBlocked)
	DefaultEvents = append(DefaultEvents, EgressBlocked)
	Kinds = append(Kinds, EgressUnknown)
	DefaultEvents = append(DefaultEvents, EgressUnknown)
}

package egress

const (
	publicEntryPrimary uint8 = iota + 1
	publicEntryMinted
	publicEntryRetired
)

// These predicates are the policy of both full selected and borrowed stored
// state. Their callers supply facts from the same canonical class, binding,
// credential-field and pinned grammar validators, with different storage.
func publicExposedFacts(hasBinding, sentinelEmpty, shapeAbsent, identityEmpty bool) bool {
	return !hasBinding && sentinelEmpty && shapeAbsent && identityEmpty
}

func publicAliasKindFacts(a, b uint8, armedA, armedB bool) bool {
	return a != publicEntryMinted && b != publicEntryMinted && (a != publicEntryPrimary || b != publicEntryPrimary || armedA == armedB)
}

func publicAliasFacts(a, b uint8, armedA, armedB, sameSentinel, sameIdentity, sameShape, sameBinding bool) bool {
	return publicAliasKindFacts(a, b, armedA, armedB) && sameSentinel && sameIdentity && sameShape && sameBinding
}

func publicMintedFacts(identityValid, rulePresent, shapePresent, sameRule, authorityAllowed bool) bool {
	return identityValid && rulePresent && shapePresent && sameRule && authorityAllowed
}

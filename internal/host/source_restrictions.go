package host

// GitSourceRestriction adds a refusal only; it grants no filesystem access.
type GitSourceRestriction struct{ Path, Identity string }

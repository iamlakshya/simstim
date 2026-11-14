package api

// IsRuleID recognises the existing public abbreviation's syntax only. It
// grants no identity or selection authority; actual rows still require the
// canonical engine's ambiguity check in their requested layer/provenance.
func IsRuleID(value string) bool {
	if len(value) != 7 || value[0] != 'g' {
		return false
	}
	for _, b := range []byte(value[1:]) {
		if !(b >= 'a' && b <= 'z' || b >= '2' && b <= '7') {
			return false
		}
	}
	return true
}

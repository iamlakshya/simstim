package api

// SecretNameIssue distinguishes invalid syntax from the reserved agent socket.
type SecretNameIssue uint8

// Canonical name validation results.
const (
	SecretNameOK SecretNameIssue = iota
	SecretNameInvalid
	SecretNameReserved
)

// SecretNameValidator retains fixed ASCII grammar state, never input text.
// A zero assignment resets it. All client, host and streamed callers share it.
type SecretNameValidator struct {
	seen, invalid, different bool
	position                 uint8
}

// Rune consumes one decoded name scalar without retaining its text.
func (s *SecretNameValidator) Rune(r rune) {
	const reserved = "SSH_AUTH_SOCK"
	letter := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_'
	if !letter && (!s.seen || r < '0' || r > '9') {
		s.invalid = true
	}
	s.seen = true
	if int(s.position) >= len(reserved) || r != rune(reserved[s.position]) {
		s.different = true
	}
	if int(s.position) <= len(reserved) {
		s.position++
	}
}

// Check applies the name grammar and the definition-only socket reservation.
func (s SecretNameValidator) Check(allowSocket bool) SecretNameIssue {
	if !s.seen || s.invalid {
		return SecretNameInvalid
	}
	if !allowSocket && !s.different && int(s.position) == len("SSH_AUTH_SOCK") {
		return SecretNameReserved
	}
	return SecretNameOK
}

// CheckSecretName validates one already owned name with the same rune state.
// allowSocket is true for tier/general-name syntax checks; definition and
// final environment policies separately decide whether a socket is permitted.
func CheckSecretName(name string, allowSocket bool) SecretNameIssue {
	var state SecretNameValidator
	for _, r := range name {
		state.Rune(r)
	}
	return state.Check(allowSocket)
}

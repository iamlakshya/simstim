package client

import "unicode"

type secretNameIssue uint8

const (
	secretNameOK secretNameIssue = iota
	secretNameInvalid
	secretNameReserved
)

// These value states retain no input text. Reset them with a zero assignment.
// The final-definition visitor and ordinary normaliser feed the same runes.
type secretNameValidation struct {
	seen, invalid, different bool
	position                 uint8
}

// Rune consumes one decoded name scalar without retaining its text.
func (s *secretNameValidation) Rune(r rune) {
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
func (s secretNameValidation) Check(allowSocket bool) secretNameIssue {
	if !s.seen || s.invalid {
		return secretNameInvalid
	}
	if !allowSocket && !s.different && int(s.position) == len("SSH_AUTH_SOCK") {
		return secretNameReserved
	}
	return secretNameOK
}

func checkSecretName(name string, allowSocket bool) secretNameIssue {
	var state secretNameValidation
	for _, r := range name {
		state.Rune(r)
	}
	return state.Check(allowSocket)
}

type secretRunValidation struct{ nonspace, nul bool }

// Rune consumes one decoded run scalar with the canonical Unicode predicate.
func (s *secretRunValidation) Rune(r rune) {
	s.nul = s.nul || r == 0
	s.nonspace = s.nonspace || !unicode.IsSpace(r)
}

// Valid requires a nonempty trimmed command and no NUL anywhere in the value.
func (s secretRunValidation) Valid() bool { return s.nonspace && !s.nul }

func usableSecretRun(command string) bool {
	var state secretRunValidation
	for _, r := range command {
		state.Rune(r)
	}
	return state.Valid()
}

type secretKindClass uint8

const (
	secretKindEmpty secretKindClass = iota
	secretKindSSHKey
	secretKindInvalid
)

type secretKindValidation struct {
	position  uint8
	different bool
}

// Rune consumes one decoded kind scalar without extracting an unknown value.
func (s *secretKindValidation) Rune(r rune) {
	const supported = "ssh-key"
	if int(s.position) >= len(supported) || r != rune(supported[s.position]) {
		s.different = true
	}
	if int(s.position) <= len(supported) {
		s.position++
	}
}

// Kind classifies the completed final scalar using the exact supported token.
func (s secretKindValidation) Kind() secretKindClass {
	if s.position == 0 {
		return secretKindEmpty
	}
	if !s.different && int(s.position) == len("ssh-key") {
		return secretKindSSHKey
	}
	return secretKindInvalid
}

func classifySecretKind(kind string) secretKindClass {
	var state secretKindValidation
	for _, r := range kind {
		state.Rune(r)
	}
	return state.Kind()
}

type secretBindingSource uint8

const (
	secretBindingNone secretBindingSource = iota
	secretBindingLocal
	secretBindingAgent
)

type secretBindingPresence struct {
	HostsPresent, HeaderNonempty, FormatPresent, FormatNonempty, FormatExplicitEmpty bool
	HostsCount                                                                       int
	DefaultHostsPresent, DefaultHeaderNonempty, DefaultFormatNonempty                bool
	DefaultHostsCount                                                                int
	Kind                                                                             secretKindClass
}

type secretBindingSelection struct {
	Hosts, Header, Format secretBindingSource
	DefaultRaw, Bound     bool
}

type secretBindingIssue uint8

const (
	secretBindingOK secretBindingIssue = iota
	secretBindingEmptyFormat
	secretBindingEmptyHosts
	secretBindingSSHKey
)

// planSecretBinding decides presence/default sources only. The one secretbind
// grammar validates their actual selected strings and HTTPS authorities.
func planSecretBinding(p secretBindingPresence) (secretBindingSelection, secretBindingIssue) {
	if p.FormatExplicitEmpty || p.FormatPresent && !p.FormatNonempty {
		return secretBindingSelection{}, secretBindingEmptyFormat
	}
	var selected secretBindingSelection
	hosts := 0
	if p.DefaultHostsPresent {
		selected.Hosts, hosts = secretBindingAgent, p.DefaultHostsCount
	}
	if p.HostsPresent {
		if p.HostsCount <= 0 {
			return secretBindingSelection{}, secretBindingEmptyHosts
		}
		selected.Hosts, hosts = secretBindingLocal, p.HostsCount
	}
	if p.DefaultHeaderNonempty {
		selected.Header = secretBindingAgent
	}
	if p.HeaderNonempty {
		selected.Header = secretBindingLocal
	}
	if p.DefaultFormatNonempty {
		selected.Format = secretBindingAgent
	}
	if p.FormatNonempty {
		selected.Format = secretBindingLocal
	}
	selected.DefaultRaw = selected.Format == secretBindingNone && hosts > 0 && selected.Header != secretBindingNone
	selected.Bound = selected.Hosts != secretBindingNone || selected.Header != secretBindingNone || selected.Format != secretBindingNone || selected.DefaultRaw
	if selected.Bound && p.Kind == secretKindSSHKey {
		return secretBindingSelection{}, secretBindingSSHKey
	}
	return selected, secretBindingOK
}

package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"unicode"
)

// Client spellings alias the sole lower API grammar used by host/env and
// final-definition visitors. The reservation policy remains caller-selected.
type secretNameIssue = api.SecretNameIssue

const (
	secretNameOK       = api.SecretNameOK
	secretNameInvalid  = api.SecretNameInvalid
	secretNameReserved = api.SecretNameReserved
)

type secretNameValidation = api.SecretNameValidator

func checkSecretName(name string, allowSocket bool) secretNameIssue {
	return api.CheckSecretName(name, allowSocket)
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

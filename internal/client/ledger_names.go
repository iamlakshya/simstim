package client

import (
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// repoHostState is the same DNS label predicate used by validRepoHost.
// All bounds below already exist in that canonical predicate.
type repoHostState struct {
	total, label int
	lastAlnum    bool
}

func (s *repoHostState) add(r rune) bool {
	s.total++
	if s.total > 253 {
		return false
	}
	if r == '.' {
		if s.label == 0 || !s.lastAlnum {
			return false
		}
		s.label, s.lastAlnum = 0, false
		return true
	}
	alnum := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
	if !alnum && r != '-' || s.label == 0 && !alnum {
		return false
	}
	s.label++
	if s.label > 63 {
		return false
	}
	s.lastAlnum = alnum
	return true
}
func (s *repoHostState) valid() bool { return s.total > 0 && s.label > 0 && s.lastAlnum }

type repoSegmentsState struct {
	length int
	dots   bool
}

func (s *repoSegmentsState) segmentValid() bool { return s.length > 0 && (!s.dots || s.length > 2) }
func (s *repoSegmentsState) add(r rune) bool {
	if r == '/' {
		if !s.segmentValid() {
			return false
		}
		s.length, s.dots = 0, false
		return true
	}
	if s.length == 0 {
		s.dots = true
	}
	s.length = min(s.length+1, 3)
	s.dots = s.dots && r == '.'
	return true
}

type canonicalRepoState struct {
	host   repoHostState
	path   repoSegmentsState
	inPath bool
}

func (s *canonicalRepoState) add(r rune) bool {
	if !s.inPath {
		if r == '/' {
			s.inPath = true
			return s.host.valid()
		}
		return s.host.add(r)
	}
	if r <= 0x20 || r == 0x7f || strings.ContainsRune(`\?#*[]`, r) {
		return false
	}
	if !s.path.add(r) {
		return false
	}

	return true
}
func (s *canonicalRepoState) valid() bool {
	return s.inPath && s.path.segmentValid()
}

// canonicalLedgerRepo validates an already canonical stored identity. Its
// path is never fed back into raw URL or trailing-suffix normalisation.
func canonicalLedgerRepo(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	var state canonicalRepoState
	for _, r := range value {
		if !state.add(r) {
			return false
		}
	}
	return state.valid()
}

func projectedLedgerName(p *api.JSONProjection, kind OutboundKind, span api.JSONSpan) error {
	if span.End == 0 {
		if kind == OutboundSSHAgent || kind == OutboundPaste {
			return nil
		}
		return badGrantStateError(grantsFileName)
	}
	null, err := p.Literal(span, "null")
	if err != nil {
		return err
	}
	if null {
		if kind == OutboundSSHAgent || kind == OutboundPaste {
			return nil
		}
		return badGrantStateError(grantsFileName)
	}
	switch kind {
	case OutboundSSHAgent, OutboundPaste:
		same, err := p.StringEqual(span, "")
		if err != nil {
			return err
		}
		if !same {
			return badGrantStateError(grantsFileName)
		}
		return nil
	case OutboundSecret, OutboundSSHKey:
		same, err := p.MatchRegexp(span, secretNameRE)
		if err != nil {
			return err
		}
		if !same {
			return badGrantStateError(grantsFileName)
		}
		return nil
	case OutboundRepo:
		var state canonicalRepoState
		if err := p.WalkString(span, func(r rune) error {
			if !state.add(r) {
				return badGrantStateError(grantsFileName)
			}
			return nil
		}); err != nil {
			return err
		}
		if !state.valid() {
			return badGrantStateError(grantsFileName)
		}
		return nil
	case OutboundAlertToken:
		if same, err := p.StringEqual(span, "ntfy"); err != nil {
			return err
		} else if same {
			return nil
		}
		var host repoHostState
		prefix := "webhook:"
		at := 0
		if err := p.WalkString(span, func(r rune) error {
			if at < len(prefix) {
				if r != rune(prefix[at]) {
					return badGrantStateError(grantsFileName)
				}
				at++
				return nil
			}
			if !host.add(r) {
				return badGrantStateError(grantsFileName)
			}
			return nil
		}); err != nil {
			return err
		}
		if at != len(prefix) || !host.valid() {
			return badGrantStateError(grantsFileName)
		}
		return nil
	default:
		return badGrantStateError(grantsFileName)
	}
}

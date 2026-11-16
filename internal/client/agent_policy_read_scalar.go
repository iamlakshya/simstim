package client

import (
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// Validation needs these scalar facts even for an unselected row. It must
// not depend on strings retained only in the selected result.
type agentPolicyRowValidity struct {
	hostIDNonempty bool
	wallNone       bool
	sshAgent       bool
}

func projectedBoundedBytes(p jsonValueProjection, span api.JSONSpan, value []byte) (int, error) {
	// Callers supply only existing semantic bounds for IDs and wall names.
	n := 0
	err := p.WalkString(span, func(r rune) error {
		if n+utf8.RuneLen(r) > len(value) {
			return badSandboxState()
		}
		n += utf8.EncodeRune(value[n:], r)
		return nil
	})
	return n, err
}

func projectedSessionID(p jsonValueProjection, span api.JSONSpan) error {
	var value [32]byte
	n, err := projectedBoundedBytes(p, span, value[:])
	if err != nil || !api.ValidSessionID(string(value[:n])) {
		return badSandboxState()
	}
	return nil
}

func projectedHostID(p jsonValueProjection, span api.JSONSpan, selected bool) (string, bool, error) {
	if span.End == 0 {
		return "", false, nil
	}
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return "", false, err
	}
	var value [32]byte
	n, err := projectedBoundedBytes(p, span, value[:])
	if err != nil || n != 0 && !ValidHostID(string(value[:n])) {
		return "", false, badSandboxState()
	}
	if selected {
		return string(value[:n]), n != 0, nil
	}
	return "", n != 0, nil
}

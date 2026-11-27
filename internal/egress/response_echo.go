package egress

import (
	"errors"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// responseEchoSnapshot is a lightweight immutable view of a shared generation.
// Its override vectors name only actually-used trusted registry identities.
type responseEchoSnapshot struct {
	base      *responseEchoGroupSnapshot
	overrides []*responseEchoOverrideVector
	maxBytes  int
}

type responseEchoPair struct{ Value, Sentinel api.Secret }

func (responseEchoPair) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoSnapshot) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newResponseEchoSnapshot(pairs []responseEchoPair) (*responseEchoSnapshot, error) {
	seen := make(map[api.Secret]api.Secret, len(pairs))
	var groups []responseEchoValueGroup
	for _, pair := range pairs {
		if pair.Value == "" || len(pair.Sentinel) != len(pair.Value) || pair.Value == pair.Sentinel {
			return nil, errors.New("invalid response echo shape. Try: rebuild the session's secret generation")
		}
		if old, ok := seen[pair.Value]; ok {
			if old != pair.Sentinel {
				return nil, errors.New("ambiguous response echo identity. Try: rebuild the session's secret generation")
			}
			continue
		}
		seen[pair.Value] = pair.Sentinel
		groups = append(groups, responseEchoValueGroup{Value: pair.Value, Choices: []responseEchoChoice{{Identity: "unambiguous-derived", Sentinel: pair.Sentinel}}})
	}
	base, err := newResponseEchoGroupSnapshot(groups)
	if err != nil {
		return nil, err
	}
	return base.view(nil)
}

type responseEchoMatch struct {
	length   int
	sentinel api.Secret
	wait     bool
	err      error
}

func (responseEchoMatch) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (s *responseEchoSnapshot) exact(value api.Secret) (api.Secret, bool) {
	if s == nil || s.base == nil || value == "" {
		return "", false
	}
	for _, index := range s.base.buckets[value[0]] {
		if s.base.groups[index].Value == value {
			sentinel, err := s.resolve(index)
			return sentinel, err == nil
		}
	}
	return "", false
}

func (s *responseEchoSnapshot) match(data []byte, eof bool) responseEchoMatch {
	var result responseEchoMatch
	if s == nil || s.base == nil || len(data) == 0 {
		return result
	}
	for _, index := range s.base.buckets[data[0]] {
		group := s.base.groups[index]
		n := min(len(data), len(group.Value))
		same := true
		for i := 0; i < n; i++ {
			if data[i] != group.Value[i] {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		if n < len(group.Value) {
			if !eof {
				result.wait = true
			}
			continue
		}
		if result.length == 0 {
			result.length = len(group.Value)
			result.sentinel, result.err = s.resolve(index)
		}
	}
	return result
}

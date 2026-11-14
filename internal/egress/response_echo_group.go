package egress

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

// Backend publishes canonical groups once per authority/generation. Values
// and valid identity choices are retained even when this response cannot yet
// resolve a group. No empty sentinel is used as an ambiguity placeholder.
type responseEchoChoice struct {
	Identity string
	Sentinel api.Secret
	Pending  bool
}
type responseEchoValueGroup struct {
	Value   api.Secret
	Choices []responseEchoChoice
}
type responseEchoGroup struct {
	Value     api.Secret
	Choices   []responseEchoChoice
	ambiguous bool
	pending   bool
}
type responseEchoOverride struct {
	group    int
	sentinel api.Secret
	pending  bool
}
type responseEchoOverrideVector struct {
	identity string
	entries  []responseEchoOverride
}
type responseEchoGroupSnapshot struct {
	groups   []responseEchoGroup
	buckets  map[byte][]int
	first    [4]uint64
	vectors  map[string]*responseEchoOverrideVector
	maxBytes int
}

func (responseEchoChoice) Format(f fmt.State, _ rune)         { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoValueGroup) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoGroup) Format(f fmt.State, _ rune)          { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoOverride) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoOverrideVector) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (responseEchoGroupSnapshot) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }

var errResponseEchoIdentity = errors.New("response echo identity is unresolved. Try: review the request's bound credentials")
var errResponseEchoPending = errors.New("response credential registration is pending. Try: repeat the request after registration finishes")

func newResponseEchoGroupSnapshot(input []responseEchoValueGroup) (*responseEchoGroupSnapshot, error) {
	merged := make(map[api.Secret]map[string]responseEchoChoice, len(input))
	for _, group := range input {
		if group.Value == "" || len(group.Choices) == 0 {
			return nil, errResponseEchoIdentity
		}
		choices := merged[group.Value]
		if choices == nil {
			choices = make(map[string]responseEchoChoice)
			merged[group.Value] = choices
		}
		for _, choice := range group.Choices {
			if choice.Identity == "" || len(choice.Sentinel) != len(group.Value) || choice.Sentinel == group.Value {
				return nil, errResponseEchoIdentity
			}
			if old, ok := choices[choice.Identity]; ok && (old.Sentinel != choice.Sentinel || old.Pending != choice.Pending) {
				return nil, errResponseEchoIdentity
			}
			choices[choice.Identity] = choice
		}
	}
	base := &responseEchoGroupSnapshot{buckets: make(map[byte][]int), vectors: make(map[string]*responseEchoOverrideVector)}
	values := make([]api.Secret, 0, len(merged))
	for value := range merged {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for _, value := range values {
		index := len(base.groups)
		group := responseEchoGroup{Value: value}
		for _, choice := range merged[value] {
			group.Choices = append(group.Choices, choice)
		}
		sort.Slice(group.Choices, func(i, j int) bool { return group.Choices[i].Identity < group.Choices[j].Identity })
		for _, choice := range group.Choices {
			group.pending = group.pending || choice.Pending
			if choice.Sentinel != group.Choices[0].Sentinel {
				group.ambiguous = true
			}
			if _, ok := base.vectors[choice.Identity]; !ok {
				base.vectors[choice.Identity] = nil
			}
		}
		base.groups = append(base.groups, group)
		base.buckets[value[0]] = append(base.buckets[value[0]], index)
		base.first[value[0]>>6] |= uint64(1) << (value[0] & 63)
		base.maxBytes = max(base.maxBytes, len(value))
		if group.ambiguous || group.pending {
			for _, choice := range group.Choices {
				vector := base.vectors[choice.Identity]
				if vector == nil {
					vector = &responseEchoOverrideVector{identity: choice.Identity}
					base.vectors[choice.Identity] = vector
				}
				vector.entries = append(vector.entries, responseEchoOverride{group: index, sentinel: choice.Sentinel, pending: choice.Pending})
			}
		}
	}
	for key, indexes := range base.buckets {
		sort.Slice(indexes, func(i, j int) bool {
			a, b := base.groups[indexes[i]].Value, base.groups[indexes[j]].Value
			if len(a) != len(b) {
				return len(a) > len(b)
			}
			return a < b
		})
		base.buckets[key] = indexes
	}
	return base, nil
}

package egress

import (
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

// Main maps actual used names to these canonical identities. They are not
// taken from a guest, header or remote row. No values or index are cloned here.
func (base *responseEchoGroupSnapshot) view(usedIdentities []string) (*responseEchoSnapshot, error) {
	if base == nil {
		return nil, errResponseEchoIdentity
	}
	view := &responseEchoSnapshot{base: base, maxBytes: base.maxBytes}
	identities := append([]string(nil), usedIdentities...)
	sort.Strings(identities)
	for i, identity := range identities {
		if i > 0 && identity == identities[i-1] {
			continue
		}
		vector, ok := base.vectors[identity]
		if !ok {
			return nil, errResponseEchoIdentity
		}
		if vector != nil {
			view.overrides = append(view.overrides, vector)
		}
	}
	return view, nil
}

func (s *responseEchoSnapshot) starts(b byte) bool {
	return s != nil && s.base != nil && s.base.first[b>>6]&(uint64(1)<<(b&63)) != 0
}

func (s *responseEchoSnapshot) resolve(index int) (api.Secret, error) {
	group := s.base.groups[index]
	if group.forbidden {
		return "", errResponseEchoIdentity
	}
	if !group.ambiguous && !group.pending {
		return group.Choices[0].Sentinel, nil
	}
	var sentinel api.Secret
	found := false
	pending, conflict := false, false
	for _, vector := range s.overrides {
		position := sort.Search(len(vector.entries), func(i int) bool { return vector.entries[i].group >= index })
		if position == len(vector.entries) || vector.entries[position].group != index {
			continue
		}
		entry := vector.entries[position]
		if entry.pending {
			pending = true
			continue
		}
		value := entry.sentinel
		if found && sentinel != value {
			conflict = true
		}
		sentinel = value
		found = true
	}
	if pending {
		return "", errResponseEchoPending
	}
	if conflict {
		return "", errResponseEchoIdentity
	}
	if !found {
		if group.pending {
			return "", errResponseEchoPending
		}
		return "", errResponseEchoIdentity
	}
	return sentinel, nil
}

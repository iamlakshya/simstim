package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

const fdNameMapKind uint8 = 255

// There are five finite DTO map roles. Their rows are offset-only records in
// the existing grouped host tree. base/count describe actual source-order
// rows, allowing a native JSON walk to query names without holding a tree
// walk open while another credential group is inserted.
type fdNameMap struct {
	id, base, count uint64
	span            api.JSONSpan
}

func (s *fdPublicState) indexMap(span api.JSONSpan) (fdNameMap, error) {
	group := fdNameMap{span: span}
	null, err := projectedNull(s.p, span)
	if err != nil || null {
		return group, err
	}
	group.id, err = s.region.AppendBinding(tomlinspect.BindingRecord{Name: fdSpan(span), FormatKind: fdNameMapKind})
	if err != nil {
		return group, err
	}
	err = projectedStoredMap(s.p, span, func(field api.JSONField) error {
		id, err := s.region.AppendHost(tomlinspect.HostRecord{BindingID: group.id, WholeSpan: fdSpan(field.Name), InteriorSpan: fdSpan(field.Name), ValueSpan: fdSpan(field.Value)})
		if err != nil {
			return err
		}
		inserted, err := s.hosts.Put(group.id, id, id)
		if err != nil {
			return err
		}
		s.duplicateMap = s.duplicateMap || !inserted
		if group.count == 0 {
			group.base = id
		}
		group.count++
		return nil
	})
	if err != nil {
		return group, err
	}
	if err := s.region.SetBindingHostCount(group.id, group.count); err != nil {
		return group, err
	}
	return group, nil
}

func (s *fdPublicState) mapSource(group fdNameMap, visit func(uint64, tomlinspect.HostRecord) error) error {
	if group.id == 0 {
		return nil
	}
	var ordinal uint64
	err := projectedStoredMap(s.p, group.span, func(field api.JSONField) error {
		if ordinal >= group.count {
			return tomlinspect.ErrIndexInvariant
		}
		id := group.base + ordinal
		record, err := s.region.Host(id)
		if err != nil {
			return err
		}
		if record.BindingID != group.id || record.WholeSpan != fdSpan(field.Name) || record.ValueSpan != fdSpan(field.Value) {
			return tomlinspect.ErrIndexInvariant
		}
		ordinal++
		return visit(id, record)
	})
	if err != nil {
		return err
	}
	if ordinal != group.count {
		return tomlinspect.ErrIndexInvariant
	}
	return nil
}

func (s *fdPublicState) mapLookup(group fdNameMap, nameRecord uint64) (tomlinspect.HostRecord, bool, error) {
	if group.id == 0 {
		return tomlinspect.HostRecord{}, false, nil
	}
	id, exists, err := s.hosts.Find(group.id, nameRecord)
	if err != nil || !exists {
		return tomlinspect.HostRecord{}, exists, err
	}
	record, err := s.region.Host(id)
	return record, true, err
}

func (s *fdPublicState) localMap(group fdNameMap, allowed api.JSONSpan) error {
	null, err := projectedNull(s.p, allowed)
	if err != nil {
		return err
	}
	var cursor api.OrderedJSONStrings
	var name api.JSONSpan
	exists := false
	if !null {
		cursor, err = s.p.OrderedStrings(allowed)
		if err != nil {
			return err
		}
		name, exists, err = cursor.Next()
		if err != nil {
			return err
		}
	}
	if group.id != 0 {
		err = s.hosts.WalkFirst(group.id, func(_ uint64, id, _ uint64) error {
			record, err := s.region.Host(id)
			if err != nil {
				return err
			}
			fieldName := jsonSpan(record.WholeSpan)
			if err := fdName(s.p, fieldName); err != nil {
				return err
			}
			for exists {
				order, err := s.p.CompareStrings(name, fieldName)
				if err != nil {
					return err
				}
				if order >= 0 {
					break
				}
				name, exists, err = cursor.Next()
				if err != nil {
					return err
				}
			}
			if !exists {
				return invalidPublicSessionState()
			}
			order, err := s.p.CompareStrings(name, fieldName)
			if err != nil {
				return err
			}
			if order != 0 {
				return invalidPublicSessionState()
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for exists {
		name, exists, err = cursor.Next()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *fdPublicState) primaries(modes, primary, binds fdNameMap) error {
	if modes.count != primary.count {
		return invalidPublicSessionState()
	}
	var boundCount uint64
	err := s.mapSource(modes, func(modeID uint64, mode tomlinspect.HostRecord) error {
		public, exists, err := s.mapLookup(primary, modeID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidPublicSessionState()
		}
		bound, err := fdStringEqual(s.p, jsonSpan(mode.ValueSpan), string(api.SecretModeBound))
		if err != nil {
			return err
		}
		exposed, err := fdStringEqual(s.p, jsonSpan(mode.ValueSpan), string(api.SecretModeExposed))
		if err != nil {
			return err
		}
		if !bound && !exposed {
			return invalidPublicSessionState()
		}
		bind, bindExists, err := s.mapLookup(binds, modeID)
		if err != nil {
			return err
		}
		var binding uint64
		if bound {
			if !bindExists {
				return invalidPublicSessionState()
			}
			boundCount++
			binding, err = s.binding(jsonSpan(mode.WholeSpan), jsonSpan(bind.ValueSpan), false)
			if err != nil {
				return err
			}
		} else if bindExists {
			return invalidPublicSessionState()
		}
		expected := api.SecretModeExposed
		if bound {
			expected = api.SecretModeBound
		}
		if err := s.entry(jsonSpan(mode.WholeSpan), jsonSpan(public.ValueSpan), fdPrimary, binding, expected); err != nil {
			return err
		}
		return s.region.SetHostReference(modeID, binding)
	})
	if err != nil {
		return err
	}
	if boundCount != binds.count {
		return invalidPublicSessionState()
	}
	return nil
}

func (s *fdPublicState) minted(minted, modes fdNameMap) error {
	return s.mapSource(minted, func(id uint64, record tomlinspect.HostRecord) error {
		owner, exists, err := s.mapLookup(modes, id)
		if err != nil {
			return err
		}
		if !exists || owner.ReferenceID == 0 {
			return invalidPublicSessionState()
		}
		binding, err := s.region.Binding(owner.ReferenceID)
		if err != nil {
			return err
		}
		if binding.FormatKind < 1 || binding.FormatKind > 3 {
			return tomlinspect.ErrIndexInvariant
		}
		return projectedStoredArray(s.p, jsonSpan(record.ValueSpan), func(value api.JSONSpan) error {
			return s.entry(jsonSpan(record.WholeSpan), value, fdMinted, owner.ReferenceID, api.SecretModeBound)
		})
	})
}

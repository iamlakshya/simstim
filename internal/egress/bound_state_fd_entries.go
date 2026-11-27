package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

const (
	fdPrimary uint8 = publicEntryPrimary
	fdMinted        = publicEntryMinted
	fdRetired       = publicEntryRetired
)

type fdEntryFields struct{ mode, armed, sentinel, identity, shape, binding, authority, rule api.JSONSpan }

func (s *fdPublicState) entryFields(span api.JSONSpan, kind uint8) (fdEntryFields, error) {
	var fields fdEntryFields
	known := s.schema.primary
	if kind == fdMinted {
		known = s.schema.minted
	}
	if kind == fdRetired {
		known = s.schema.retired
	}
	err := fdFields(s.p, span, known, func(key string, value api.JSONSpan) error {
		switch key {
		case "mode":
			fields.mode = value
		case "armed":
			fields.armed = value
		case "sentinel":
			fields.sentinel = value
		case "identity":
			fields.identity = value
		case "shape":
			fields.shape = value
		case "binding":
			fields.binding = value
		case "authority":
			fields.authority = value
		case "rule":
			fields.rule = value
		}
		return nil
	})
	return fields, err
}

func (s *fdPublicState) entry(name, span api.JSONSpan, kind uint8, binding uint64, expected api.SecretMode) error {
	fields, err := s.entryFields(span, kind)
	if err != nil {
		return err
	}
	armed := false
	if kind == fdPrimary {
		matching, err := fdStringEqual(s.p, fields.mode, string(expected))
		if err != nil {
			return err
		}
		if !matching {
			return invalidPublicSessionState()
		}
		armed, err = fdBool(s.p, fields.armed)
		if err != nil {
			return err
		}
		if expected == api.SecretModeExposed {
			sentinelEmpty, err := fdStringEqual(s.p, fields.sentinel, "")
			if err != nil {
				return err
			}
			identityEmpty, err := fdStringEqual(s.p, fields.identity, "")
			if err != nil {
				return err
			}
			shapeAbsent, err := projectedNull(s.p, fields.shape)
			if err != nil {
				return err
			}
			if !publicExposedFacts(binding != 0, sentinelEmpty, shapeAbsent, identityEmpty) {
				return invalidPublicSessionState()
			}
			return nil
		}
	}
	if err := fdNonce(s.p, fields.identity); err != nil {
		return err
	}
	if kind == fdRetired {
		binding, err = s.binding(name, fields.binding, true)
		if err != nil {
			return err
		}
	}
	if binding == 0 {
		return invalidPublicSessionState()
	}
	shape, err := s.shape(name, fields.shape, binding, kind == fdMinted)
	if err != nil {
		return err
	}
	if kind == fdMinted {
		sameRule, err := fdStringEqual(s.p, fields.rule, shape.metrics.rule)
		if err != nil {
			return err
		}
		window := api.JSONSpan{Start: fields.authority.Start + 1, End: fields.authority.End - 1}
		authority, err := s.authority(fields.authority, window)
		if err != nil {
			return err
		}
		matching, err := s.p.StringEqual(fields.authority, authority)
		if err != nil {
			return err
		}
		if !matching {
			return invalidPublicSessionState()
		}
		// Each actual minted authority is a charged source record. It is
		// queried, never inserted into its owner's allowed host relation.
		id, err := s.region.AppendHost(tomlinspect.HostRecord{BindingID: binding, WholeSpan: fdSpan(fields.authority), InteriorSpan: fdSpan(window)})
		if err != nil {
			return err
		}
		_, found, err := s.hosts.Find(binding, id)
		if err != nil {
			return err
		}
		if !publicMintedFacts(true, shape.metrics.rule != "", true, sameRule, found) {
			return invalidPublicSessionState()
		}
	}
	if err := s.sentinel(fields.sentinel, shape, binding); err != nil {
		return err
	}
	if err := s.grammar(shape, span); err != nil {
		return err
	}
	id, err := s.region.AppendEntry(tomlinspect.SemanticEntry{Kind: kind, Armed: armed, Name: fdSpan(name), Sentinel: fdSpan(fields.sentinel), Identity: fdSpan(fields.identity), Prefix: fdSpan(shape.prefix), Rule: fdSpan(shape.rule), BindingID: binding, SlotBase: shape.base, SlotCount: shape.count})
	if err != nil {
		return err
	}
	return s.accept(id)
}

func (s *fdPublicState) entryCompare(identity bool, a, b uint64) (int, error) {
	x, err := s.region.Entry(a)
	if err != nil {
		return 0, err
	}
	y, err := s.region.Entry(b)
	if err != nil {
		return 0, err
	}
	left, right := x.Sentinel, y.Sentinel
	if identity {
		left, right = x.Identity, y.Identity
	}
	return s.p.CompareStrings(jsonSpan(left), jsonSpan(right))
}

func (s *fdPublicState) sameEntry(a, b tomlinspect.SemanticEntry) (bool, error) {
	if !publicAliasKindFacts(a.Kind, b.Kind, a.Armed, b.Armed) {
		return false, nil
	}
	for _, pair := range [][2]tomlinspect.Span{{a.Sentinel, b.Sentinel}, {a.Identity, b.Identity}} {
		order, err := s.p.CompareStrings(jsonSpan(pair[0]), jsonSpan(pair[1]))
		if err != nil || order != 0 {
			return false, err
		}
	}
	equal, err := s.sameShape(a, b)
	if err != nil || !equal {
		return false, err
	}
	sameBinding, err := s.sameBinding(a.BindingID, b.BindingID)
	return publicAliasFacts(a.Kind, b.Kind, a.Armed, b.Armed, true, true, true, sameBinding), err
}

func (s *fdPublicState) accept(id uint64) error {
	current, err := s.region.Entry(id)
	if err != nil {
		return err
	}
	for _, tree := range []tomlinspect.Tree{s.sentinels, s.identities} {
		oldID, found, err := tree.Find(id, 0)
		if err != nil {
			return err
		}
		if found {
			old, err := s.region.Entry(oldID)
			if err != nil {
				return err
			}
			equal, err := s.sameEntry(old, current)
			if err != nil {
				return err
			}
			if !equal {
				return invalidPublicSessionState()
			}
		}
	}
	if _, err := s.sentinels.Put(id, 0, id); err != nil {
		return err
	}
	_, err = s.identities.Put(id, 0, id)
	return err
}

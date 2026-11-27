package egress

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// Validate checks one known stored public state inside the actual complete
// settings/state inspection. The caller owns complete root JSON validation
// and exact original local-domain equality. allowedNames is that receipt's
// canonical ordered local array. No fetch, consent, receipt or authority is
// constructed here; every source record and relation lives in the SAME store.
func (schema PublicStateProjectionSchema) Validate(ctx context.Context, p *api.JSONProjection, state, allowedNames api.JSONSpan, h *tomlinspect.Inspection) (failure error) {
	core := false
	defer func() {
		if core && errors.Is(failure, ErrUnsupportedPublicState) {
			failure = invalidPublicSessionState()
		}
	}()
	owned, err := h.Context()
	if err != nil {
		return err
	}
	source, err := h.Source(1)
	if err != nil {
		return err
	}
	if p == nil || !p.MatchesSource(owned, source, source.Size()) || !p.MatchesSource(ctx, source, source.Size()) || len(schema.primary) == 0 || len(schema.minted) == 0 || len(schema.slot) == 0 {
		return invalidPublicSessionState()
	}
	if err := h.CheckSpan(1, fdSpan(state)); err != nil {
		return err
	}
	if err := h.CheckSpan(1, fdSpan(allowedNames)); err != nil {
		return err
	}
	if err := source.Check(owned); err != nil {
		return err
	}
	s := &fdPublicState{ctx: owned, p: p, h: h, schema: schema, shapes: savedShapeJSONSchema{shape: schema.shape, slot: schema.slot}}
	// Complete DTO support classification must precede every sticky count or
	// region mutation. An unrelated future receipt can then be preserved by
	// its real owner without clearing or swallowing a Core failure.
	if err := s.classifyDTO(state, fdDTOState); err != nil {
		return err
	}
	fields, err := s.stateFields(state)
	if err != nil {
		return err
	}
	version, err := projectedNativeInt(p, fields.version)
	if err != nil {
		return err
	}
	if version != api.SessionSecretStateV1 {
		return ErrUnsupportedPublicState
	}
	intent, err := s.intentFields(fields.intent)
	if err != nil {
		return err
	}
	version, err = projectedNativeInt(p, intent.version)
	if err != nil {
		return err
	}
	if version != api.SecretDeliveryIntentV1 {
		return ErrUnsupportedPublicState
	}
	core = true
	s.charge, err = h.CountState(1, fdSpan(state), func(counter tomlinspect.StateCounter) error { return schema.Count(owned, p, state, counter) })
	if err != nil {
		return err
	}
	s.region, err = h.BeginState(s.charge)
	if err != nil {
		return err
	}
	defer s.region.Close()
	s.sentinels, err = s.region.NewTree(tomlinspect.RelationSentinel, func(a, _ uint64, b, _ uint64) (int, error) { return s.entryCompare(false, a, b) })
	if err != nil {
		return err
	}
	s.identities, err = s.region.NewTree(tomlinspect.RelationIdentity, func(a, _ uint64, b, _ uint64) (int, error) { return s.entryCompare(true, a, b) })
	if err != nil {
		return err
	}
	s.hosts, err = s.region.NewTree(tomlinspect.RelationHosts, s.hostCompare)
	if err != nil {
		return err
	}
	var groups [6]fdNameMap
	for index, span := range []api.JSONSpan{intent.modes, intent.binds, fields.primary, fields.minted, fields.retired, intent.trusted} {
		groups[index], err = s.indexMap(span)
		if err != nil {
			return err
		}
	}
	if s.duplicateMap {
		// Put reports duplicate keys as data, without a Core failure. Retire
		// every region and recheck the source before allowing its owner to
		// preserve an unrelated opaque receipt. No sticky error is cleared.
		if err := s.region.FinishState(); err != nil {
			return err
		}
		if err := source.Check(owned); err != nil {
			return err
		}
		core = false
		return ErrUnsupportedPublicState
	}
	if err := fdNonce(p, fields.generation); err != nil {
		return err
	}
	for _, group := range groups[:5] {
		if err := s.localMap(group, allowedNames); err != nil {
			return err
		}
	}
	trustedSchema := TrustedUseProjectionSchemaV1{use: schema.trusted, binding: schema.binding}
	if err := s.mapSource(groups[5], func(_ uint64, record tomlinspect.HostRecord) error {
		if err := ValidateTrustedSecretNameProjectionV1(p, jsonSpan(record.WholeSpan)); err != nil {
			return err
		}
		_, err := trustedSchema.Project(owned, p, jsonSpan(record.ValueSpan), false)
		return err
	}); err != nil {
		return err
	}
	if err := s.primaries(groups[0], groups[2], groups[1]); err != nil {
		return err
	}
	if err := s.minted(groups[3], groups[0]); err != nil {
		return err
	}
	if err := s.mapSource(groups[4], func(_ uint64, record tomlinspect.HostRecord) error {
		return projectedStoredArray(p, jsonSpan(record.ValueSpan), func(value api.JSONSpan) error {
			return s.entry(jsonSpan(record.WholeSpan), value, fdRetired, 0, api.SecretModeBound)
		})
	}); err != nil {
		return err
	}
	if err := s.region.FinishState(); err != nil {
		return err
	}
	if !p.MatchesSource(owned, source, source.Size()) {
		return invalidPublicSessionState()
	}
	return source.Check(owned)
}

package client

import (
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// This view owns no parser/source copy. Only Object adds a mandatory exact
// same-source preflight; every other codec method delegates to the ONE parser.
type agentJSONProjection struct {
	*api.JSONProjection
	inspection *tomlinspect.Inspection
}

func newAgentJSONProjection(p *api.JSONProjection, h *tomlinspect.Inspection) (*agentJSONProjection, error) {
	ctx, err := h.Context()
	if err != nil {
		return nil, err
	}
	source, err := h.Source(1)
	if err != nil {
		return nil, err
	}
	if p == nil || source.Kind() != tomlinspect.MetadataSandboxJSON || !p.MatchesSource(ctx, source, source.Size()) {
		return nil, tomlinspect.ErrIndexInvariant
	}
	return &agentJSONProjection{JSONProjection: p, inspection: h}, nil
}

func (p *agentJSONProjection) CompareObjectNames(a, b tomlinspect.Span) (int, error) {
	return p.JSONProjection.CompareStrings(api.JSONSpan{Start: a.Start, End: a.End}, api.JSONSpan{Start: b.Start, End: b.End})
}

func (p *agentJSONProjection) Object(span api.JSONSpan, known []string, visit func(api.JSONField) error) error {
	if p == nil || p.JSONProjection == nil || p.inspection == nil || visit == nil {
		return tomlinspect.ErrIndexInvariant
	}
	if err := p.preflightObject(span, known); err != nil {
		return err
	}
	// The actual source-bound object preflight finished and retired BEFORE
	// any visitor/child object runs. No boolean or cached permission skips it.
	fields, err := p.JSONProjection.Fields(span)
	if err != nil {
		return err
	}
	for {
		field, more, err := fields.Next()
		if err != nil || !more {
			return err
		}
		field.Known, err = p.JSONProjection.KnownName(field.Name, known)
		if err != nil {
			return err
		}
		if err := visit(field); err != nil {
			return err
		}
	}
}

func (p *agentJSONProjection) preflightObject(span api.JSONSpan, known []string) (err error) {
	charge, err := p.inspection.CountObject(1, tomlinspect.Span{Start: span.Start, End: span.End}, func(counter tomlinspect.ObjectCounter) error {
		fields, err := p.JSONProjection.Fields(span)
		if err != nil {
			return err
		}
		for {
			field, more, err := fields.Next()
			if err != nil || !more {
				return err
			}
			// Alias/Unicode/error facts come from the SAME canonical matcher.
			if _, err := p.JSONProjection.KnownName(field.Name, known); err != nil {
				return err
			}
			if err := counter.Field(); err != nil {
				return err
			}
		}
	})
	if err != nil {
		return err
	}
	object, err := p.inspection.BeginObject(charge, p)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, object.Close()) }()
	fields, err := p.JSONProjection.Fields(span)
	if err != nil {
		return err
	}
	duplicate := false
	for {
		field, more, err := fields.Next()
		if err != nil {
			return err
		}
		if !more {
			break
		}
		inserted, err := object.PutName(tomlinspect.Span{Start: field.Name.Start, End: field.Name.End})
		if err != nil {
			return err
		}
		duplicate = duplicate || !inserted
	}
	if err := object.FinishObject(); err != nil {
		return err
	}
	if duplicate {
		return api.ErrJSONProjection
	}
	return nil
}

var _ jsonValueProjection = (*agentJSONProjection)(nil)
var _ tomlinspect.ObjectNameComparer = (*agentJSONProjection)(nil)

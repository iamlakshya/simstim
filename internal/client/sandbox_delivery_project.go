package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

type sandboxDeliveryContext struct {
	hostIDNonempty bool
	wallNone       bool
	sshAgent       bool
	futureLineage  bool
}

// This is the one stored envelope grammar for full and bounded readers.
// Binding scalar semantics come only from the canonical P23 validators.
func projectSandboxDelivery(ctx context.Context, p jsonValueProjection, span, allowed, exposed api.JSONSpan, schema int, row sandboxDeliveryContext, retain bool) (*api.SecretDeliveryIntent, bool, error) {
	if span.End == 0 {
		return nil, false, nil
	}
	if schema != 4 {
		return nil, false, badSandboxStateContext(ctx)
	}
	var fields [4]api.JSONSpan
	err := p.Object(span, []string{"version", "modes", "binds", "trusted_uses"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	var versionBytes [19]byte
	n := 0
	err = p.WalkEncoded(fields[0], func(part []byte) error {
		if len(part) > len(versionBytes)-n {
			return badSandboxStateContext(ctx)
		}
		n += copy(versionBytes[n:], part)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	version, err := sandboxLineageVersion(versionBytes[:n])
	if err != nil {
		return nil, false, err
	}
	if version > api.SecretDeliveryIntentV1 {
		// Preserve future metadata without claiming that a legacy agent bit
		// grants authority. Every consumer must refuse FutureDelivery.
		return nil, true, nil
	}
	if row.futureLineage {
		return nil, false, badSandboxStateContext(ctx)
	}
	var kept *api.SecretDeliveryIntent
	if retain {
		kept = &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	}
	modes, err := p.OrderedFields(fields[1])
	if err != nil {
		return nil, false, err
	}
	binds, err := p.OrderedFields(fields[2])
	if err != nil {
		return nil, false, err
	}
	names, err := deliveryNamesCursor(p, allowed)
	if err != nil {
		return nil, false, err
	}
	exposures, err := deliveryNamesCursor(p, exposed)
	if err != nil {
		return nil, false, err
	}
	name, nok, err := names.next()
	if err != nil {
		return nil, false, err
	}
	exposure, eok, err := exposures.next()
	if err != nil {
		return nil, false, err
	}
	binding, bok, err := binds.Next()
	if err != nil {
		return nil, false, err
	}
	for {
		field, more, err := modes.Next()
		if err != nil {
			return nil, false, err
		}
		if !more {
			break
		}
		if err := projectedSecretName(p, field.Name); err != nil {
			return nil, false, err
		}
		reserved, err := p.StringEqual(field.Name, "SSH_AUTH_SOCK")
		if err != nil || reserved || !nok {
			return nil, false, badSandboxStateContext(ctx)
		}
		order, err := p.CompareStrings(name, field.Name)
		if err != nil || order != 0 {
			return nil, false, badSandboxStateContext(ctx)
		}
		var spelling [7]byte
		n, err := projectedBoundedBytes(p, field.Value, spelling[:])
		mode := api.SecretMode(spelling[:n])
		if err != nil || !mode.Valid() {
			return nil, false, badSandboxStateContext(ctx)
		}
		if mode == api.SecretModeBound {
			mode = api.SecretModeBound
		} else {
			mode = api.SecretModeExposed
		}
		if eok {
			order, err := p.CompareStrings(exposure, field.Name)
			if err != nil || order < 0 || order == 0 && mode != api.SecretModeExposed {
				return nil, false, badSandboxStateContext(ctx)
			}
			if order == 0 {
				exposure, eok, err = exposures.next()
				if err != nil {
					return nil, false, err
				}
			}
		}
		var keptBinding api.SecretBind
		if mode == api.SecretModeBound {
			if !row.hostIDNonempty || row.wallNone || row.sshAgent || !bok {
				return nil, false, badSandboxStateContext(ctx)
			}
			order, err := p.CompareStrings(binding.Name, field.Name)
			if err != nil || order != 0 {
				return nil, false, badSandboxStateContext(ctx)
			}
			keptBinding, err = projectSandboxBind(ctx, p, binding.Value, retain)
			if err != nil {
				return nil, false, err
			}
			binding, bok, err = binds.Next()
			if err != nil {
				return nil, false, err
			}
		}
		if retain {
			key, err := retainProjectedString(p, field.Name)
			if err != nil {
				return nil, false, err
			}
			kept.Modes[key] = mode
			if mode == api.SecretModeBound {
				kept.Binds[key] = keptBinding
			}
		}
		name, nok, err = names.next()
		if err != nil {
			return nil, false, err
		}
	}
	if nok || bok || eok {
		return nil, false, badSandboxStateContext(ctx)
	}
	if fields[3].End != 0 {
		null, err := p.Literal(fields[3], "null")
		if err != nil {
			return nil, false, err
		}
		if !null {
			schema := egress.NewTrustedUseProjectionSchemaV1()
			future := false
			err := p.Object(fields[3], nil, func(field api.JSONField) error {
				if !row.hostIDNonempty || row.wallNone || row.sshAgent {
					return badSandboxStateContext(ctx)
				}
				if err := egress.ValidateTrustedSecretNameProjectionV1(p, field.Name); err != nil {
					return err
				}
				use, err := schema.Project(ctx, p, field.Value, retain)
				if errors.Is(err, egress.ErrUnsupportedPublicState) {
					future = true
					return nil
				}
				if err != nil {
					return err
				}
				if retain {
					name, err := retainProjectedString(p, field.Name)
					if err != nil {
						return err
					}
					if kept.TrustedUses == nil {
						kept.TrustedUses = map[string]api.TrustedSecretUseV1{}
					}
					kept.TrustedUses[name] = use
				}
				return nil
			})
			if err != nil {
				return nil, false, err
			}
			if future {
				return nil, true, nil
			}
		}
	}
	return kept, false, ctx.Err()
}

type deliveryNames struct {
	iterator api.OrderedJSONStrings
	empty    bool
}

func deliveryNamesCursor(p jsonValueProjection, span api.JSONSpan) (deliveryNames, error) {
	if span.End == 0 {
		return deliveryNames{empty: true}, nil
	}
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return deliveryNames{empty: true}, err
	}
	iterator, err := p.OrderedStrings(span)
	return deliveryNames{iterator: iterator}, err
}

func (i *deliveryNames) next() (api.JSONSpan, bool, error) {
	if i.empty {
		return api.JSONSpan{}, false, nil
	}
	return i.iterator.Next()
}

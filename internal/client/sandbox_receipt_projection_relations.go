package client

import "github.com/iamlakshya/simstim/internal/api"

type receiptPublicStateSpans struct{ generation, modes, binds api.JSONSpan }

func receiptStateSpans(p *api.JSONProjection, span api.JSONSpan) (receiptPublicStateSpans, error) {
	var out receiptPublicStateSpans
	var intent api.JSONSpan
	known := api.ExactJSONFieldNames((*api.SessionSecretState)(nil))
	err := p.Object(span, known, func(f api.JSONField) error {
		if f.Known >= 0 {
			switch known[f.Known] {
			case "generation":
				out.generation = f.Value
			case "intent":
				intent = f.Value
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	known = api.ExactJSONFieldNames((*api.SecretDeliveryIntent)(nil))
	err = p.Object(intent, known, func(f api.JSONField) error {
		if f.Known >= 0 {
			switch known[f.Known] {
			case "modes":
				out.modes = f.Value
			case "binds":
				out.binds = f.Value
			}
		}
		return nil
	})
	return out, err
}

type sandboxReceiptRelationGroup uint8

const (
	receiptStateBinds sandboxReceiptRelationGroup = iota
	receiptOriginalModes
	receiptOriginalBinds
	receiptSupplied
	receiptMissing
	receiptExposed
)

type sandboxReceiptRelationPlan struct{ Groups [6]api.JSONSpan }
type sandboxReceiptRelationLookup func(sandboxReceiptRelationGroup, api.JSONSpan) (api.JSONSpan, bool, error)
type sandboxReceiptRelationPhase func(sandboxReceiptRelationPlan, func(sandboxReceiptRelationLookup) error) error

// Only finite span descriptors cross into the SAME counted temporary index.
// The complete canonical State validator has already closed its region.
func makeReceiptRelationPlan(p *api.JSONProjection, state receiptPublicStateSpans, row receiptRowProjection, supplied, missing api.JSONSpan) (sandboxReceiptRelationPlan, bool, error) {
	var plan sandboxReceiptRelationPlan
	plan.Groups[receiptStateBinds], plan.Groups[receiptSupplied], plan.Groups[receiptMissing] = state.binds, supplied, missing
	if row.delivery.End != 0 {
		if err := p.Object(row.delivery, []string{"version", "modes", "binds"}, func(f api.JSONField) error {
			if f.Known == 1 {
				plan.Groups[receiptOriginalModes] = f.Value
			} else if f.Known == 2 {
				plan.Groups[receiptOriginalBinds] = f.Value
			}
			return nil
		}); err != nil {
			return plan, false, err
		}
	}
	wallNone := true
	if row.wall.End != 0 {
		if err := p.Object(row.wall, []string{"level"}, func(f api.JSONField) error {
			if f.Known == 0 {
				var err error
				wallNone, err = p.StringEqual(f.Value, "none")
				return err
			}
			return nil
		}); err != nil {
			return plan, false, err
		}
	}
	if row.delivery.End == 0 && !wallNone {
		var err error
		plan.Groups[receiptExposed], err = deliveryExposedSpan(p, row.wall)
		if err != nil {
			return plan, false, err
		}
	}
	for group := receiptStateBinds; group <= receiptOriginalBinds; group++ {
		if plan.Groups[group].End != 0 {
			null, err := p.Literal(plan.Groups[group], "null")
			if err != nil {
				return plan, false, err
			}
			if null {
				plan.Groups[group] = api.JSONSpan{}
			}
		}
	}
	return plan, wallNone, nil
}

func receiptArrayCount(p *api.JSONProjection, span api.JSONSpan) (uint64, error) {
	values, err := p.Values(span)
	if err != nil {
		return 0, err
	}
	var count uint64
	for {
		_, more, err := values.Next()
		if err != nil {
			return 0, err
		}
		if !more {
			return count, nil
		}
		count++
	}
}

// Raw source-order map cursor plus SAME-index lookups gives bounded/log-index
// relations for arbitrary JSON map ordering. No ordered-map assumption, body
// allocation, per-name source restart, dense fallback or second tree.
func checkIndexedReceiptRelations(p *api.JSONProjection, state receiptPublicStateSpans, originalDelivery, wallNone bool, supplied, missing, allowed api.JSONSpan, lookup sandboxReceiptRelationLookup) error {
	if lookup == nil {
		return sandboxReceiptError()
	}
	for _, part := range []api.JSONSpan{supplied, missing} {
		ok, err := p.OrderedStringArraySubset(part, allowed)
		if err != nil {
			return err
		}
		if !ok {
			return sandboxReceiptError()
		}
	}
	a, err := receiptArrayCount(p, supplied)
	if err != nil {
		return err
	}
	b, err := receiptArrayCount(p, missing)
	if err != nil {
		return err
	}
	fields, err := p.Fields(state.modes)
	if err != nil {
		return err
	}
	var count uint64
	for {
		field, more, err := fields.Next()
		if err != nil {
			return err
		}
		if !more {
			break
		}
		count++
		_, sent, err := lookup(receiptSupplied, field.Name)
		if err != nil {
			return err
		}
		_, absent, err := lookup(receiptMissing, field.Name)
		if err != nil {
			return err
		}
		if sent == absent {
			return sandboxReceiptError()
		}
		if !originalDelivery {
			exposed, err := p.StringEqual(field.Value, string(api.SecretModeExposed))
			if err != nil {
				return err
			}
			if !exposed {
				return sandboxReceiptError()
			}
			if !wallNone {
				_, found, err := lookup(receiptExposed, field.Name)
				if err != nil {
					return err
				}
				if !found {
					return sandboxReceiptError()
				}
			}
			continue
		}
		original, found, err := lookup(receiptOriginalModes, field.Name)
		if err != nil {
			return err
		}
		if !found {
			return sandboxReceiptError()
		}
		order, err := p.CompareStrings(field.Value, original)
		if err != nil {
			return err
		}
		if order != 0 {
			return sandboxReceiptError()
		}
		bound, err := p.StringEqual(field.Value, string(api.SecretModeBound))
		if err != nil {
			return err
		}
		if !bound {
			continue
		}
		x, xok, err := lookup(receiptStateBinds, field.Name)
		if err != nil {
			return err
		}
		y, yok, err := lookup(receiptOriginalBinds, field.Name)
		if err != nil {
			return err
		}
		if !xok || !yok {
			return sandboxReceiptError()
		}
		equal, err := projectReceiptBindEqual(p, x, y)
		if err != nil {
			return err
		}
		if !equal {
			return sandboxReceiptError()
		}
	}
	if count != a+b {
		return sandboxReceiptError()
	}
	return nil
}

// Hosts and finite binding scalars already passed canonical binding grammar.
// This is exact typed tuple equality only, not another binding validator.
func projectReceiptBindEqual(p *api.JSONProjection, a, b api.JSONSpan) (bool, error) {
	known := api.ExactJSONFieldNames((*api.SecretBind)(nil))
	var left, right [3]api.JSONSpan
	collect := func(span api.JSONSpan, out *[3]api.JSONSpan) error {
		return p.Object(span, known, func(f api.JSONField) error {
			if f.Known >= 0 {
				out[f.Known] = f.Value
			}
			return nil
		})
	}
	if err := collect(a, &left); err != nil {
		return false, err
	}
	if err := collect(b, &right); err != nil {
		return false, err
	}
	for i, key := range known {
		if key != "hosts" {
			order, err := p.CompareStrings(left[i], right[i])
			if err != nil || order != 0 {
				return false, err
			}
			continue
		}
		x, err := p.Values(left[i])
		if err != nil {
			return false, err
		}
		y, err := p.Values(right[i])
		if err != nil {
			return false, err
		}
		for {
			xs, xok, err := x.Next()
			if err != nil {
				return false, err
			}
			ys, yok, err := y.Next()
			if err != nil {
				return false, err
			}
			if xok != yok {
				return false, nil
			}
			if !xok {
				break
			}
			order, err := p.CompareStrings(xs, ys)
			if err != nil || order != 0 {
				return false, err
			}
		}
	}
	return true, nil
}

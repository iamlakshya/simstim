package egress

import (
	"errors"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

var errClassifierUnicode = errors.New("classifier Unicode data or canonical name is unsupported. Try: update matching helpers or grant the intended exact host")

type classifierName struct {
	host     string
	wildcard bool
}

// This immutable index names possible comparisons, never grants. Every hit
// is checked against current locks, expiry, methods and once availability.
type classifierNameIndex map[string][]classifierName

func classifierSkeleton(host string) (string, error) {
	// UTS39 revision26 and its complete Unicode15 data match the tables
	// actually active under the pinned Go1.26 toolchain. Later UTS39 versions
	// change this algorithm; a future migration must update both together.
	if norm.Version != "15.0.0" {
		return "", errClassifierUnicode
	}
	prototypes, err := loadClassifierConfusables()
	if err != nil {
		return "", err
	}
	decoded, err := idna.Lookup.ToUnicode(host)
	if err != nil {
		return "", errClassifierUnicode
	}
	canonical, err := idna.Lookup.ToASCII(decoded)
	if err != nil || canonical != host {
		return "", errClassifierUnicode
	}
	var skeleton strings.Builder
	for _, r := range norm.NFD.String(decoded) {
		if mapped, exists := prototypes[r]; exists {
			skeleton.WriteString(mapped)
		} else {
			skeleton.WriteRune(r)
		}
	}
	return norm.NFD.String(skeleton.String()), nil
}

func buildClassifierNameIndex(engine *Engine) (classifierNameIndex, error) {
	index := make(classifierNameIndex)
	seen := make(map[classifierName]bool)
	add := func(rules []compiledRule) error {
		for _, rule := range rules {
			if !rule.allow || rule.uploadBudget || rule.isNumeric || compiledDoor(rule) != api.DoorNet {
				continue
			}
			name := classifierName{host: rule.host, wildcard: rule.wildcard}
			if seen[name] {
				continue
			}
			seen[name] = true
			skeleton, err := classifierSkeleton(name.host)
			if err != nil {
				return err
			}
			index[skeleton] = append(index[skeleton], name)
		}
		return nil
	}
	for _, layer := range engine.layers {
		if err := add(layer.rules); err != nil {
			return nil, err
		}
	}
	if err := add(engine.agentRules); err != nil {
		return nil, err
	}
	if engine.preset == api.PresetBalanced {
		if err := add(engine.balanced); err != nil {
			return nil, err
		}
	}
	return index, nil
}

// The ledger lock is already held by the actual evaluator. The probe uses
// that evaluator's read-only mode and cannot enter classification recursively.
func (e *Engine) classifyConfusableLocked(request Request, host string) Decision {
	labels := strings.Split(host, ".")
	for suffix := range labels {
		observed := strings.Join(labels[suffix:], ".")
		skeleton, err := classifierSkeleton(observed)
		if err != nil {
			return Decision{Rule: "classify:normalise"}
		}
		for _, name := range e.classifierNames[skeleton] {
			if suffix != 0 && !name.wildcard || suffix == 0 && name.wildcard || observed == name.host {
				continue
			}
			candidate := name.host
			if suffix != 0 {
				candidate = strings.Join(labels[:suffix], ".") + "." + candidate
			}
			probe := request
			probe.Host, probe.classifierProbe = candidate, true
			decision := e.evaluateLocked(probe, nil, evalPrecheck)
			if decision.Allow && !decision.unmatched {
				return Decision{Rule: "classify:confusable", classifiedRefusal: true}
			}
		}
	}
	return Decision{Rule: "classify:unsure", unmatched: true}
}

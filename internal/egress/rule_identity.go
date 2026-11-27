package egress

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CanonicalRuleInstances admits and owns complete rule instances before a
// direct engine can bind a ledger. It is the same grammar and equality used
// by layer cleanup; surviving independent leases are never silently merged.
func CanonicalRuleInstances(rules []api.Rule) ([]api.Rule, error) {
	if err := ValidateRuleIdentities(rules); err != nil {
		return nil, err
	}
	if rules == nil {
		return nil, nil
	}
	type effectiveInstance struct {
		instance ruleInstanceIdentity
		id       string
	}
	unique := make(map[effectiveInstance]api.Rule)
	for _, input := range rules {
		rule, err := canonicalIdentityRule(input)
		if err != nil {
			return nil, err
		}
		if rule.ID == "" {
			rule.ID = api.RuleID(rule)
		}
		key := effectiveInstance{instance: ruleInstanceKey(rule), id: rule.ID}
		unique[key] = rule
	}
	out := make([]api.Rule, 0, len(unique))
	for _, rule := range unique {
		out = append(out, rule)
	}
	slices.SortFunc(out, compareRuleInstances)
	return out, nil
}

// MatchesRuleReplacement is the existing explicit-add tuple: action and
// grant attributes may change, while door/target/mode/method/address scope matches.
// This helper creates neither a grant instance nor a permit.
func MatchesRuleReplacement(old, proposed api.Rule) (bool, error) {
	left, err := canonicalIdentityRule(old)
	if err != nil {
		return false, err
	}
	right, err := canonicalIdentityRule(proposed)
	if err != nil {
		return false, err
	}
	a, b := ruleHashKey(left), ruleHashKey(right)
	return a.door == b.door && a.target == b.target && a.mode == b.mode && a.constrained == b.constrained && a.methods == b.methods && a.tools == b.tools && a.publicOnly == b.publicOnly, nil
}

// CheckRuleAddition previews an existing session addition/regrant, including
// policy-wide ambiguity and locks, without entropy, mutation or a once ledger.
func CheckRuleAddition(policy api.Policy, proposed api.Rule) error {
	if err := ValidateRuleIdentities(policy.Rules); err != nil {
		return err
	}
	proposed.Layer, proposed.Name, proposed.Source = api.LayerSession, "", ""
	proposed, err := canonicalIdentityRule(proposed)
	if err != nil {
		return err
	}
	proposed.ID = api.RuleID(proposed)
	next := make([]api.Rule, 0, len(policy.Rules)+1)
	for _, old := range policy.Rules {
		if old.Layer == api.LayerSession && old.Name == "" && old.Source == "" {
			match, err := MatchesRuleReplacement(old, proposed)
			if err != nil {
				return err
			}
			if match && !(old.Allow && old.Once && proposed.Allow && proposed.Once) {
				continue
			}
		}
		next = append(next, old)
	}
	next = append(next, proposed)
	if err := ValidateRuleIdentities(next); err != nil {
		return err
	}
	var layers []api.Layer
	for _, lock := range policy.Locks {
		copy := lock
		layers = append(layers, api.Layer{Kind: lock.Layer, Name: lock.Name, Lock: &copy})
	}
	layers = append(layers, api.Layer{Kind: api.LayerSession, Rules: []api.Rule{proposed}})
	_, _, err = MergeLayers(layers)
	return err
}

// RuleID is a display/selection abbreviation, never an equality key. This
// structured identity mirrors its existing fields without truncation or
// delimiter ambiguity. Source and lease instances remain separate below.
type ruleHashIdentity struct {
	layer       api.LayerKind
	name        string
	allow       bool
	publicOnly  bool
	door        api.Door
	target      string
	mode        string
	constrained bool
	methods     string
	tools       string
}

type ruleInstanceIdentity struct {
	base    ruleHashIdentity
	source  api.LayerSource
	once    bool
	until   time.Time
	grantID string
}

func canonicalIdentityRule(rule api.Rule) (api.Rule, error) {
	if err := canonicalRulePublicOnly(rule); err != nil {
		return api.Rule{}, err
	}
	if rule.Door == "" {
		rule.Door = api.DoorNet
	}
	methods, err := canonicalRuleMethods(rule)
	if err != nil {
		return api.Rule{}, err
	}
	rule.Methods = methods
	tools, err := canonicalRuleTools(rule)
	if err != nil {
		return api.Rule{}, err
	}
	rule.Tools = tools
	rule.Until = canonicalInstant(rule.Until)
	if rule.Door == api.DoorNet {
		target, err := ValidateRuleTarget(rule.Target)
		if err != nil {
			return api.Rule{}, err
		}
		rule.Target = target
	}
	if rule.Door == api.DoorPort {
		if _, err := api.ParsePortTarget(rule.Target); err != nil || rule.Mode != "" || rule.Once || rule.Methods != nil || len(rule.Tools) != 0 {
			return api.Rule{}, &proto.Error{Code: "bad_port", Message: "the port rule is not exact and supported", Hint: "Use one decimal port without methods, tools, mode or once."}
		}
	}
	if rule.Door == api.DoorPush {
		return canonicalPushRule(rule)
	}
	return rule, nil
}

// CanonicalRuleSelection normalises one actual row for selection or repair,
// without admitting the surrounding policy or creating a grant. Existing
// supplied IDs cannot hide a collision under the canonical abbreviation.
func CanonicalRuleSelection(rule api.Rule) (api.Rule, error) {
	canonical, err := canonicalIdentityRule(rule)
	if err != nil {
		return api.Rule{}, err
	}
	canonical.ID = api.RuleID(canonical)
	return canonical, nil
}

func ruleHashKey(rule api.Rule) ruleHashIdentity {
	door := rule.Door
	if door == "" {
		door = api.DoorNet
	}
	key := ruleHashIdentity{layer: rule.Layer, name: rule.Name, allow: rule.Allow, door: door, target: rule.Target, mode: rule.Mode, publicOnly: rule.PublicOnly}
	if rule.Methods != nil {
		key.constrained = true
		// Valid exact HTTP tokens cannot contain NUL. Presence distinguishes
		// the empty constrained set from the unrestricted legacy form.
		key.methods = strings.Join(*rule.Methods, "\x00")
	}
	key.tools = strings.Join(rule.Tools, "\x00")
	return key
}

func ruleInstanceKey(rule api.Rule) ruleInstanceIdentity {
	return ruleInstanceIdentity{base: ruleHashKey(rule), source: rule.Source, once: rule.Once, until: canonicalInstant(rule.Until), grantID: rule.GrantID}
}

func ambiguousRuleID(id string) error {
	return &proto.Error{Code: "rule_id_ambiguous", Message: "policy rule ID " + id + " identifies different rules",
		Hint: "Edit one colliding rule before approving, applying or removing this ID. Public rule IDs are abbreviations."}
}

// ValidateRuleIdentities refuses a public/effective ID shared by different
// canonical base tuples. Equal base identity with a different grant instance
// is not a hash collision. This check allocates no engine or once ledger.
func ValidateRuleIdentities(rules []api.Rule) error {
	seen := make(map[string]ruleHashIdentity)
	for _, input := range rules {
		rule, err := canonicalIdentityRule(input)
		if err != nil {
			return err
		}
		canonicalID := api.RuleID(rule)
		if (rule.Methods != nil || len(rule.Tools) != 0 || rule.PublicOnly) && rule.ID != "" && rule.ID != canonicalID {
			return &proto.Error{Code: "bad_rule_id", Message: "method-qualified rule has a noncanonical ID", Hint: "Remove the supplied rule ID and submit the rule again."}
		}
		key := ruleHashKey(rule)
		for _, id := range []string{canonicalID, rule.ID} {
			if id == "" {
				continue
			}
			if old, exists := seen[id]; exists && old != key {
				return ambiguousRuleID(id)
			}
			seen[id] = key
		}
	}
	return nil
}

// EqualRuleInstances compares full effective rule and lease data after the
// one canonical target/method grammar. Public ID spelling is not equality.
func EqualRuleInstances(a, b api.Rule) (bool, error) {
	a, err := canonicalIdentityRule(a)
	if err != nil {
		return false, err
	}
	b, err = canonicalIdentityRule(b)
	if err != nil {
		return false, err
	}
	return ruleInstanceKey(a) == ruleInstanceKey(b), nil
}

// CheckRuleIDSelection checks one actual layer/provenance without choosing
// a first row or lease. Callers retain their existing ID-based operation only
// when every matching abbreviation identifies the same complete base tuple.
func CheckRuleIDSelection(rules []api.Rule, id string, scope api.LayerRef) (bool, error) {
	if !api.ValidLayerRef(scope) {
		return false, badLayerOrder()
	}
	var chosen ruleHashIdentity
	found := false
	for _, input := range rules {
		if input.Layer != scope.Kind || input.Name != scope.Name || input.Source != scope.Source {
			continue
		}
		rule, err := canonicalIdentityRule(input)
		if err != nil {
			return false, err
		}
		effectiveID := rule.ID
		if effectiveID == "" {
			effectiveID = api.RuleID(rule)
		}
		if effectiveID != id {
			continue
		}
		key := ruleHashKey(rule)
		if found && key != chosen {
			return false, ambiguousRuleID(id)
		}
		chosen, found = key, true
	}
	return found, nil
}

func compareRuleInstances(a, b api.Rule) int {
	if order := cmp.Compare(a.ID, b.ID); order != 0 {
		return order
	}
	left, right := ruleInstanceKey(a), ruleInstanceKey(b)
	for _, pair := range [][2]string{
		{string(left.base.layer), string(right.base.layer)}, {left.base.name, right.base.name},
		{string(left.source), string(right.source)}, {string(left.base.door), string(right.base.door)},
		{left.base.target, right.base.target}, {left.base.mode, right.base.mode},
		{left.base.methods, right.base.methods}, {left.base.tools, right.base.tools},
	} {
		if order := cmp.Compare(pair[0], pair[1]); order != 0 {
			return order
		}
	}
	for _, pair := range [][2]bool{{left.base.allow, right.base.allow}, {left.base.publicOnly, right.base.publicOnly}, {left.base.constrained, right.base.constrained}, {left.once, right.once}} {
		if pair[0] != pair[1] {
			if !pair[0] {
				return -1
			}
			return 1
		}
	}
	if order := left.until.Compare(right.until); order != 0 {
		return order
	}
	return cmp.Compare(left.grantID, right.grantID)
}

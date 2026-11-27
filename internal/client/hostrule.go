package client

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// hostRulePlanInput is selection data from the original local request and its
// checked settings. The operation owner supplies these values before dialing.
// Neither this input nor its result can issue a grant, facts or admission.
type hostRulePlanInput struct {
	argument    string
	rule        *api.HostSelectorV1
	ruleLayer   api.LayerKind
	ruleName    string
	environment string
	saved       string
	last        string
}

type hostRulePlan struct {
	names     []string
	automatic bool
	reason    string
	defaulted bool // presentation only, never named-request or pool authority
}

// resolveHostRulePlan applies only the local precedence of spec 18.2. A list
// retains the request's order. The real New owner must make fresh, source-bound
// attempts and report actual refusals. This function performs no I/O or wake.
func resolveHostRulePlan(in hostRulePlanInput, hosts []Host, tiers Tiers) (hostRulePlan, error) {
	if in.argument != "" {
		name, reason := in.argument, "named by the request"
		if name == "auto" {
			return hostRulePlan{automatic: true, reason: "automatic placement requested"}, nil
		}
		if name == "-" {
			if in.last == "" {
				return hostRulePlan{}, &UserError{Msg: "no session has been attached yet", Hint: brand.Name + " new <host>", Code: "no_last_session", Kind: KindNotFound}
			}
			name, reason = in.last, "from the last attached session"
		}
		if !slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == name }) {
			return hostRulePlan{}, unknownHostError(name, hosts)
		}
		if _, err := findPlacementHost(hosts, name); err != nil {
			return hostRulePlan{}, err
		}
		return hostRulePlan{names: []string{name}, reason: reason}, nil
	}
	if in.rule != nil {
		if err := in.rule.Validate(); err != nil {
			return hostRulePlan{}, invalidHostRule()
		}
		reason, err := hostRuleReason(in.ruleLayer, in.ruleName)
		if err != nil {
			return hostRulePlan{}, err
		}
		if in.rule.Auto {
			return hostRulePlan{automatic: true, reason: reason}, nil
		}
		positions := make(map[string]int, len(hosts))
		for i, host := range hosts {
			if _, duplicate := positions[host.Name]; duplicate {
				positions[host.Name] = -1
			} else {
				positions[host.Name] = i
			}
		}
		for _, name := range in.rule.Names {
			rows := hosts
			if position, found := positions[name]; found && position >= 0 {
				rows = hosts[position : position+1]
			}
			// Reuse the one saved-host lookup for grammar and ambiguity. The
			// local index makes a long valid rule linear in hosts plus names.
			if _, err := findPlacementHost(rows, name); err != nil {
				return hostRulePlan{}, err
			}
		}
		return hostRulePlan{names: slices.Clone(in.rule.Names), reason: reason}, nil
	}
	name, reason := in.environment, "from the default-host environment variable"
	if name == "" {
		name, reason = in.saved, "from the saved default host"
	}
	if name == "auto" {
		return hostRulePlan{automatic: true, reason: reason, defaulted: true}, nil
	}
	if name != "" {
		host, err := findPlacementHost(hosts, name)
		if err != nil {
			return hostRulePlan{}, err
		}
		if err := allowDefaultHost(host, tiers.Of(name), Outbound{Kind: OutboundDefaultHost}); err != nil {
			return hostRulePlan{}, err
		}
		return hostRulePlan{names: []string{name}, reason: reason, defaulted: true}, nil
	}
	var only string
	for _, host := range hosts {
		if !ValidHostName(host.Name) || tiers.Of(host.Name) == TierShared {
			continue
		}
		if only != "" {
			return hostRulePlan{}, hostRuleSelectionRequired(hosts)
		}
		only = host.Name
	}
	if only == "" {
		return hostRulePlan{}, hostRuleSelectionRequired(hosts)
	}
	return hostRulePlan{names: []string{only}, reason: "the only saved host that is not shared"}, nil
}

func hostRuleReason(layer api.LayerKind, name string) (string, error) {
	switch layer {
	case api.LayerSession:
		return "from the session's host rule", nil
	case api.LayerProfile:
		if name == "" {
			return "", invalidHostRule()
		}
		return "from the profile " + CleanLine(name), nil
	case api.LayerRepo:
		prefix, wildcard, err := ValidateRepoPattern(name)
		if err != nil {
			return "", err
		}
		if wildcard {
			prefix += "/*"
		}
		return "from the rule for " + prefix, nil
	default:
		return "", &UserError{Msg: "sandbox.host cannot select a host from this layer",
			Hint: "Put the host rule in a profile, repository section or the session's --set values.", Code: "host_rule_layer", Kind: KindUsage}
	}
}

func invalidHostRule() error {
	return &UserError{Msg: "sandbox.host needs a host name, a non-empty list of distinct host names or auto",
		Hint: "Check sandbox.host in the selected profile, repository section or --set value.", Code: "bad_value", Kind: KindUsage}
}

func hostRuleSelectionRequired(hosts []Host) error {
	var names []string
	for _, host := range hosts {
		if ValidHostName(host.Name) {
			names = append(names, host.Name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	message := "no host was selected"
	if len(names) != 0 {
		message += ". Saved hosts: " + strings.Join(names, ", ")
	}
	return &UserError{Msg: message, Hint: fmt.Sprintf("%s new <host>, or %s host default <host>", brand.Name, brand.Name), Code: "host_required", Kind: KindUsage}
}

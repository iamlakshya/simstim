package egress

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func canonicalRuleMethods(rule api.Rule) (*[]string, error) {
	if rule.Methods != nil && rule.Door != "" && rule.Door != api.DoorNet {
		return nil, &proto.Error{Code: "bad_method", Message: "HTTP methods apply only to network rules", Hint: "Use --method only with a network rule."}
	}
	methods, err := api.CanonicalHTTPMethods(rule.Methods)
	if err != nil {
		return nil, &proto.Error{Code: "bad_method", Message: err.Error(), Hint: "Pass each method as one exact HTTP token, such as GET or POST."}
	}
	return methods, nil
}

func matchesRuleMethod(rule compiledRule, method string) bool {
	if rule.methods == nil {
		return true
	}
	// An opaque TCP/CONNECT flow has no inspected HTTP method. A method
	// constraint cannot grant a tunnel merely because its label is CONNECT.
	return method != "" && slices.Contains(*rule.methods, method)
}

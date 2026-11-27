package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func canonicalRulePublicOnly(rule api.Rule) error {
	if rule.PublicOnly && (!rule.Allow || rule.Door != "" && rule.Door != api.DoorNet) {
		return &proto.Error{Code: "bad_public_only", Message: "public-only addresses restrict a network allowance", Hint: "Use public_only only on an allow rule for the network door."}
	}
	return nil
}

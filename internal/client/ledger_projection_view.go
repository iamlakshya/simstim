package client

import (
	"regexp"

	"github.com/iamlakshya/simstim/internal/api"
)

// A ledger's bounded and ordinary readers share the same predicates and ONE
// parser. Only the bounded Object method adds the real same-store preflight.
// This method view grants no record, Source, fetch or outbound permission.
type ledgerValueProjection interface {
	jsonValueProjection
	MatchRegexp(api.JSONSpan, *regexp.Regexp) (bool, error)
}

var _ ledgerValueProjection = (*api.JSONProjection)(nil)
var _ ledgerValueProjection = (*agentJSONProjection)(nil)

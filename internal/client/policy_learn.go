package client

import (
	"context"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicyLearnCandidate is an observation for explicit selection, never a grant.
type PolicyLearnCandidate struct {
	Target string `json:"target"`
	Flows  uint64 `json:"flows"`
}

func policyLearnLimit() error {
	return &UserError{Msg: "the policy observations exceed one bounded learning review", Hint: "Review a shorter audit period or split the observations before learning rules.", Code: "policy_learn_limit", Kind: KindRefused}
}

func addPolicyLearnEntries(counts map[string]uint64, entries []api.NetAuditEntry) error {
	for _, entry := range entries {
		eligible := entry.Decision == "allow-unknown" && entry.Rule == "unknown:allow" || entry.Decision == "allow" && (entry.By == "live" || entry.By == "classifier")
		if !eligible || entry.Kind != "net" || entry.Port < 1 || entry.Port > 65535 || entry.Host == "" || api.CleanText(entry.Host, 253) != entry.Host {
			continue
		}
		// Audit masks are display data. Only a complete canonical host can
		// become a proposed target, and it still needs explicit selection.
		target, err := egress.ValidateRuleTarget(net.JoinHostPort(entry.Host, strconv.Itoa(entry.Port)))
		if err != nil {
			continue
		}
		if _, exists := counts[target]; !exists && len(counts) >= 4096 {
			return policyLearnLimit()
		}
		if counts[target] == math.MaxUint64 {
			return policyLearnLimit()
		}
		counts[target]++
	}
	return nil
}

// PolicyLearnCandidates consumes bounded cursor pages without dropping equal
// flow records. A missing cursor capability refuses rather than truncating.
func (c *Conn) PolicyLearnCandidates(ctx context.Context, session string) ([]PolicyLearnCandidate, error) {
	counts := map[string]uint64{}
	args := api.PolicyLogArgs{ID: session, Door: api.DoorNet, Limit: api.PolicyLogMaxLimit}
	for pages := 0; ; pages++ {
		if pages >= 4096 {
			return nil, policyLearnLimit()
		}
		page, err := c.PolicyLogPage(ctx, args)
		if err != nil {
			return nil, err
		}
		if err := addPolicyLearnEntries(counts, page.Entries); err != nil {
			return nil, err
		}
		if !page.More {
			break
		}
		args.Cursor = page.Cursor
	}
	result := make([]PolicyLearnCandidate, 0, len(counts))
	for target, count := range counts {
		result = append(result, PolicyLearnCandidate{target, count})
	}
	slices.SortFunc(result, func(a, b PolicyLearnCandidate) int { return strings.Compare(a.Target, b.Target) })
	return result, nil
}

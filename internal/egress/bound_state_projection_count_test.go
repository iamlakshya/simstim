package egress

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type publicStateCounts struct{ entries, bindings, hosts, slots int }

func (c *publicStateCounts) Entry() error   { c.entries++; return nil }
func (c *publicStateCounts) Binding() error { c.bindings++; return nil }
func (c *publicStateCounts) Host() error    { c.hosts++; return nil }
func (c *publicStateCounts) Slot() error    { c.slots++; return nil }

func TestProjectedStateCountsEveryPhysicalShapeSlotAcrossAllHistories(t *testing.T) {
	const source = `{"version":1,"generation":"count-only","intent":{"version":1,"modes":{"A":"bound"},"binds":{"A":{"hosts":["example.com:443"],"header":"Authorization","format":"Bearer {value}"}}},"primary":{"A":{"shape":{"slots":[{},{}]}}},"minted":{"A":[{"shape":{"slots":[{},{},{}]}},{"shape":null}]},"retired":{"A":[{"shape":{"slots":[{},{},{},{}]},"binding":{"hosts":["other.example:443"],"header":"Authorization","format":"Bearer {value}"}}]}}`
	p, err := api.NewJSONProjection(t.Context(), strings.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}
	span, err := p.Root()
	if err != nil {
		t.Fatal(err)
	}
	counts := new(publicStateCounts)
	if err := NewPublicStateProjectionSchema().Count(t.Context(), p, span, counts); err != nil {
		t.Fatal(err)
	}
	if *counts != (publicStateCounts{entries: 4, bindings: 7, hosts: 7, slots: 9}) {
		t.Fatalf("physical state records were undercharged: %+v", counts)
	}
	// Deliberately invalid semantic fields above prove that a count is only
	// a structural charge. The real semantic validator still must reject it.
}

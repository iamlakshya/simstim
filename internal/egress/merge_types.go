package egress

import (
	"net/netip"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// Shared canonical merge declarations are moved once from runtime policy/once.
// Full runtime composition removes these same declarations from their old files.
type compiledRule struct {
	ref        api.LayerRef
	id         string
	allow      bool
	host       string
	wildcard   bool
	isNumeric  bool
	prefix     netip.Prefix
	port       int // 0 means default: 443 on allow, any port on deny
	path       string
	pathPrefix bool
	methods    *[]string
	once       bool
	until      time.Time
	grantID    string
	slot       *onceSlot
}

type onceKey struct {
	id      string
	grantID string
	until   time.Time
}

type onceSlot struct {
	key   onceKey
	state slotState
	owner *Permit
}

type onceLedger struct {
	mu    sync.Mutex
	slots map[onceKey]*onceSlot
}

// Permit holds reservations for one flow. It carries no engine or cached
// verdict. Every recheck evaluates the current engine's rules.
type Permit struct {
	ledger    *onceLedger
	owned     map[*onceSlot]struct{}
	committed bool
	closed    bool
}

type slotState uint8

const (
	slotAvailable slotState = iota
	slotReserved
	slotCommitted
	slotConsumed
)

func canonicalInstant(at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	return at.Round(0).UTC()
}

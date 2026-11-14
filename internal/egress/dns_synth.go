package egress

import (
	"encoding/binary"
	"net/netip"
	"sync"
)

// Synthetic address pool bounds within 198.18.0.0/15 (spec 7.2 and 20.10).
// Network 198.18.0.0 and broadcast 198.19.255.255 are excluded, and 198.18.0.1
// stays reserved for the guest dummy interface.
const (
	// MaxSyntheticNames caps per-session synthetic host mappings inside the
	// egress memory budget.
	MaxSyntheticNames = 8192

	syntheticFirstUint32 uint32 = (198 << 24) | (18 << 16) | 2
	syntheticLastUint32  uint32 = (198 << 24) | (19 << 16) | (255 << 8) | 254
	maxSyntheticPoolSize        = int(syntheticLastUint32 - syntheticFirstUint32 + 1)
)

// SyntheticDummyAddr is the reserved 198.18.0.1 address kept out of host
// allocations for the guest dummy interface.
var SyntheticDummyAddr = netip.MustParseAddr("198.18.0.1")

type syntheticTable struct {
	mu     sync.RWMutex
	limit  int
	next   uint32
	byHost map[string]netip.Addr
	byAddr map[netip.Addr]string
}

func newSyntheticTable(limit int) *syntheticTable {
	if limit <= 0 {
		limit = MaxSyntheticNames
	}
	if limit > maxSyntheticPoolSize {
		limit = maxSyntheticPoolSize
	}
	return &syntheticTable{
		limit:  limit,
		next:   syntheticFirstUint32,
		byHost: make(map[string]netip.Addr),
		byAddr: make(map[netip.Addr]string),
	}
}

func (t *syntheticTable) assign(host string) (netip.Addr, bool) {
	t.mu.RLock()
	if addr, ok := t.byHost[host]; ok {
		t.mu.RUnlock()
		return addr, true
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	if addr, ok := t.byHost[host]; ok {
		return addr, true
	}
	if len(t.byHost) >= t.limit || t.next > syntheticLastUint32 {
		return netip.Addr{}, false
	}
	var octets [4]byte
	binary.BigEndian.PutUint32(octets[:], t.next)
	t.next++
	addr := netip.AddrFrom4(octets)
	t.byHost[host] = addr
	t.byAddr[addr] = host
	return addr, true
}

func (t *syntheticTable) lookupHost(addr netip.Addr) (string, bool) {
	if !addr.IsValid() || addr.Zone() != "" {
		return "", false
	}
	u := addr.Unmap()
	if !u.Is4() {
		return "", false
	}
	t.mu.RLock()
	host, ok := t.byAddr[u]
	t.mu.RUnlock()
	return host, ok
}

// SyntheticHost returns the canonical hostname mapped to a session synthetic
// IPv4 address, if any. It is metadata for transparent flow attribution and
// never authorises a flow on its own.
func (s *Server) SyntheticHost(addr netip.Addr) (string, bool) {
	s.mu.RLock()
	synth := s.synth
	s.mu.RUnlock()
	if synth == nil {
		return "", false
	}
	return synth.lookupHost(addr)
}

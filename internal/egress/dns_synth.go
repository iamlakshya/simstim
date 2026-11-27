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
	byHost map[string]destinationRecord
	byAddr map[netip.Addr]string
	byID   []string
}

// One existing bounded destination record may have no synthetic route.
// The ordinal index aliases its host string and carries no permission.
type destinationRecord struct {
	addr      netip.Addr
	id        uint64
	unknown   bool
	announced bool
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
		byHost: make(map[string]destinationRecord),
		byAddr: make(map[netip.Addr]string),
	}
}

func (t *syntheticTable) assign(host string) (netip.Addr, bool) {
	t.mu.RLock()
	if record, ok := t.byHost[host]; ok && record.addr.IsValid() {
		t.mu.RUnlock()
		return record.addr, true
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	record, exists := t.byHost[host]
	if exists && record.addr.IsValid() {
		return record.addr, true
	}
	if !exists && len(t.byHost) >= t.limit || t.next > syntheticLastUint32 {
		return netip.Addr{}, false
	}
	var octets [4]byte
	binary.BigEndian.PutUint32(octets[:], t.next)
	t.next++
	addr := netip.AddrFrom4(octets)
	if !exists {
		record.id = t.addIDLocked(host)
	}
	record.addr = addr
	t.byHost[host] = record
	t.byAddr[addr] = host
	return addr, true
}

func (t *syntheticTable) addIDLocked(host string) uint64 {
	if len(t.byID) == cap(t.byID) {
		capacity := min(t.limit, max(1, 2*cap(t.byID)))
		next := make([]string, len(t.byID), capacity)
		copy(next, t.byID)
		clear(t.byID)
		t.byID = next
	}
	t.byID = append(t.byID, host)
	return uint64(len(t.byID))
}

// reserveUnknown uses the existing name capacity without allocating a route
// or address. A later synthetic DNS answer can fill the same record's addr.
func (t *syntheticTable) reserveUnknown(host string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, exists := t.byHost[host]
	if !exists {
		if len(t.byHost) >= t.limit {
			return false
		}
		record.id = t.addIDLocked(host)
	}
	record.unknown = true
	t.byHost[host] = record
	return true
}

func (t *syntheticTable) unknownID(host string) uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	record := t.byHost[host]
	if !record.unknown {
		return 0
	}
	return record.id
}

// acceptUnknown marks only a destination whose actual audit was accepted.
// Every caller uses the current actor's existing private control stream.
func (t *syntheticTable) acceptUnknown(id uint64) (first, known bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == 0 || id > uint64(len(t.byID)) {
		return false, false
	}
	host := t.byID[id-1]
	record, exists := t.byHost[host]
	if !exists || !record.unknown {
		return false, false
	}
	if record.announced {
		return false, true
	}
	record.announced = true
	t.byHost[host] = record
	return true, true
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

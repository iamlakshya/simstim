package guest

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// This bounded guest cache is a routing HINT only. Trusted egress still
// authorises and resolves every name. Eviction requests a fresh DNS lookup;
// no policy/once/resolver decision lives in this guest table.
const vmSyntheticCacheEntries = dnsMaxWireBytes / 16

type vmSyntheticEntry struct {
	name  string
	until time.Time
}
type vmSyntheticMap struct {
	mu      sync.Mutex
	entries map[netip.Addr]vmSyntheticEntry
	now     func() time.Time
}

func newVMSyntheticMap() *vmSyntheticMap {
	return &vmSyntheticMap{entries: map[netip.Addr]vmSyntheticEntry{}, now: time.Now}
}

func (m *vmSyntheticMap) observe(query, response []byte) error {
	var q dnsmessage.Message
	if err := q.Unpack(query); err != nil || len(q.Questions) != 1 {
		return fmt.Errorf("invalid VM DNS question")
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(response); err != nil || !answer.Header.Response || answer.Header.ID != q.Header.ID || len(answer.Questions) != 1 || answer.Questions[0] != q.Questions[0] {
		return fmt.Errorf("VM DNS answer does not match its question")
	}
	name := strings.ToLower(strings.TrimSuffix(q.Questions[0].Name.String(), "."))
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for address, entry := range m.entries {
		if !entry.until.After(now) {
			delete(m.entries, address)
		}
	}
	for _, resource := range answer.Answers {
		value, ok := resource.Body.(*dnsmessage.AResource)
		if !ok {
			continue
		}
		address := netip.AddrFrom4(value.A)
		if !netip.MustParsePrefix("198.18.0.0/15").Contains(address) {
			continue
		}
		ttl := min(resource.Header.TTL, uint32(300))
		if ttl == 0 {
			continue
		}
		if _, exists := m.entries[address]; !exists && len(m.entries) >= vmSyntheticCacheEntries {
			var oldest netip.Addr
			var until time.Time
			for candidate, entry := range m.entries {
				if !oldest.IsValid() || entry.until.Before(until) {
					oldest, until = candidate, entry.until
				}
			}
			delete(m.entries, oldest)
		}
		m.entries[address] = vmSyntheticEntry{name: name, until: now.Add(time.Duration(ttl) * time.Second)}
	}
	return nil
}

func (m *vmSyntheticMap) host(address netip.Addr) (string, error) {
	address = address.Unmap()
	if !netip.MustParsePrefix("198.18.0.0/15").Contains(address) {
		return address.String(), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[address]
	if !ok || !entry.until.After(m.now()) {
		delete(m.entries, address)
		return "", fmt.Errorf("the VM synthetic address needs a fresh DNS lookup")
	}
	return entry.name, nil
}

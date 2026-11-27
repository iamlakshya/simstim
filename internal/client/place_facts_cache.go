package client

import (
	"context"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type placementFactsCacheV1 struct {
	Version int                         `json:"v"`
	Hosts   map[string]placementFactsV1 `json:"hosts"`
}

type placementFactsV1 struct {
	HostID     string        `json:"host_id"`
	Generation string        `json:"source_generation"`
	ReceivedAt time.Time     `json:"received_at"`
	RoundTrip  time.Duration `json:"round_trip"`
	Facts      api.HostFacts `json:"facts"`
}

// cachedPlacementFacts is explicitly cached selection data. It is not an
// accepted connection observation, and cannot replace a fresh post-wake read.
type cachedPlacementFacts struct {
	host   Host
	source hostStateSource
	entry  placementFactsV1
}

func (cached *cachedPlacementFacts) check(ctx context.Context) error {
	if cached == nil || cached.entry.ReceivedAt.IsZero() || cached.entry.Generation != cached.source.generation {
		return placementStoreError(errPlacementState)
	}
	current, err := captureHostSelectionSource(ctx, cached.host)
	if err != nil {
		return err
	}
	if !sameHostTransportSource(current, cached.source) {
		return grantIdentityError(cached.host)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, found := tiers.Entry(cached.host.Name)
	if !found || entry.HostID != cached.entry.HostID || entry.IDChanged {
		return grantIdentityError(cached.host)
	}
	return ctx.Err()
}

func decodePlacementFactsCache(raw []byte) (placementFactsCacheV1, error) {
	var cache placementFactsCacheV1
	version, err := api.JSONObjectFields(raw, "v")
	if err != nil || json.Unmarshal(version["v"], &cache.Version) != nil {
		return cache, placementStoreError(errPlacementState)
	}
	if cache.Version != 1 {
		return cache, hostFactsFutureError()
	}
	if !api.ExactJSONFields(raw, cache) || json.Unmarshal(raw, &cache) != nil || cache.Hosts == nil {
		return cache, placementStoreError(errPlacementState)
	}
	for name, entry := range cache.Hosts {
		if !ValidHostName(name) || !ValidHostID(entry.HostID) || !ValidHostID(entry.Generation) || entry.ReceivedAt.IsZero() || entry.RoundTrip < 0 {
			return cache, placementStoreError(errPlacementState)
		}
		if err := validateHostFacts(entry.Facts, entry.HostID); err != nil {
			return cache, err
		}
	}
	return cache, nil
}

func (s *placementStore) cachedFacts(ctx context.Context, host Host) (*cachedPlacementFacts, error) {
	source, err := captureHostSelectionSource(ctx, host)
	if err != nil {
		return nil, err
	}
	var selected *cachedPlacementFacts
	err = s.locked(ctx, "facts.json", func(owned context.Context) error {
		raw, exists, err := s.read(owned, "facts.json")
		if err != nil || !exists {
			return err
		}
		cache, err := decodePlacementFactsCache(raw)
		if err != nil {
			return err
		}
		entry, found := cache.Hosts[host.Name]
		if found && entry.Generation == source.generation {
			selected = &cachedPlacementFacts{host: cloneObservationHost(host), source: source, entry: entry}
		}
		return nil
	})
	if err != nil || selected == nil {
		return nil, err
	}
	if err := selected.check(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

// Cache only selection fields. Opaque runtime version/facts and descriptive
// host text are not needed for placement and are not persisted by this cache.
func placementCacheFacts(facts api.HostFacts) api.HostFacts {
	facts = cloneHostFacts(facts)
	for i := range facts.Walls {
		facts.Walls[i].Version, facts.Walls[i].Reason, facts.Walls[i].Fix = "", "", ""
		facts.Walls[i].Facts = nil
	}
	for i := range facts.Health {
		id := facts.Health[i].ID
		if id != "runtime-missing" && id != "memory_accounting" {
			id = "observed-condition"
		}
		facts.Health[i] = api.Health{ID: id, Severity: facts.Health[i].Severity, Levels: facts.Health[i].Levels}
	}
	return facts
}

func (s *placementStore) rememberFacts(ctx context.Context, observations []*hostFactsObservation, hosts []Host) error {
	for _, observation := range observations {
		if err := observation.check(ctx); err != nil {
			return err
		}
	}
	return s.locked(ctx, "facts.json", func(owned context.Context) error {
		cache := placementFactsCacheV1{Version: 1, Hosts: map[string]placementFactsV1{}}
		raw, exists, err := s.read(owned, "facts.json")
		if err != nil {
			return err
		}
		if exists {
			cache, err = decodePlacementFactsCache(raw)
			if err != nil {
				return err // Unknown or malformed data is retained, never reset.
			}
		}
		allowed := make(map[string]bool, len(hosts))
		for _, host := range hosts {
			allowed[host.Name] = true
		}
		for name := range cache.Hosts {
			if !allowed[name] {
				delete(cache.Hosts, name)
			}
		}
		for _, observed := range observations {
			if !allowed[observed.host.Name] {
				continue
			}
			cache.Hosts[observed.host.Name] = placementFactsV1{HostID: observed.hostID, Generation: observed.source.generation,
				ReceivedAt: observed.receivedAt, RoundTrip: observed.roundTrip, Facts: placementCacheFacts(observed.facts)}
		}
		return s.write(owned, "facts.json", cache)
	})
}

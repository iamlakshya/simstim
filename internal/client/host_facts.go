package client

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// hostFactsObservation comes only from an actual reply on the original saved
// connection. It is selection metadata, never a capacity reservation or start
// grant. The selected host must still perform fresh locked New admission.
type hostFactsObservation struct {
	host       Host
	hostID     string
	source     hostStateSource
	receivedAt time.Time
	roundTrip  time.Duration
	facts      api.HostFacts
}

// HostFacts reads versioned advisory observations. It neither binds a value
// request to a candidate nor wakes, prepares or reserves anything on a host.
func (c *Conn) HostFacts(ctx context.Context) (api.HostFacts, error) {
	observed, err := c.readHostFacts(ctx)
	if err != nil {
		return api.HostFacts{}, err
	}
	return cloneHostFacts(observed.facts), nil
}

func (c *Conn) readHostFacts(ctx context.Context) (*hostFactsObservation, error) {
	if err := checkFactsConnection(ctx, c); err != nil {
		return nil, err
	}
	stamp := c.hostTransport
	if !c.HasFeature("facts") {
		return nil, c.grantHelperOld("report current host facts")
	}
	started := time.Now()
	var raw json.RawMessage
	if err := c.Call(ctx, "host.facts", struct{}{}, &raw); err != nil {
		return nil, err
	}
	roundTrip := time.Since(started)
	facts, err := decodeHostFacts(raw, c.HostID())
	if err != nil {
		return nil, err
	}
	if c.hostTransport != stamp {
		return nil, grantIdentityError(c.Host)
	}
	if err := checkFactsConnection(ctx, c); err != nil {
		return nil, err
	}
	return &hostFactsObservation{host: cloneObservationHost(c.Host), hostID: c.HostID(), source: stamp.source,
		receivedAt: time.Now(), roundTrip: roundTrip, facts: cloneHostFacts(facts)}, nil
}

func checkFactsConnection(ctx context.Context, c *Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	stamp := c.hostTransport
	if stamp.kind != hostTransportSaved || !ValidHostID(c.HostID()) || c.HostIDChanged() || c.agentOnly {
		return grantIdentityError(c.Host)
	}
	// The source check ended before the RPC. No HostLock, new OP or value
	// selection remains acquired while gathering facts from pool candidates.
	return ctx.Err()
}

func (o *hostFactsObservation) check(ctx context.Context) error {
	if o == nil || !ValidHostID(o.hostID) || o.facts.HostID != o.hostID || o.facts.Version != api.HostFactsVersion || o.receivedAt.IsZero() {
		return hostFactsProtocolError()
	}
	source, err := captureHostSelectionSource(ctx, o.host)
	if err != nil {
		return err
	}
	if !sameHostTransportSource(source, o.source) {
		return grantIdentityError(o.host)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, found := tiers.Entry(o.host.Name)
	if !found || entry.HostID != o.hostID || entry.IDChanged {
		return grantIdentityError(o.host)
	}
	return ctx.Err()
}

func cloneHostFacts(facts api.HostFacts) api.HostFacts {
	facts.Walls = slices.Clone(facts.Walls)
	for i := range facts.Walls {
		facts.Walls[i].Facts = maps.Clone(facts.Walls[i].Facts)
	}
	facts.RuntimeDigests = slices.Clone(facts.RuntimeDigests)
	facts.ImageDigests = slices.Clone(facts.ImageDigests)
	facts.Health = slices.Clone(facts.Health)
	for i := range facts.Health {
		facts.Health[i].Levels = slices.Clone(facts.Health[i].Levels)
	}
	return facts
}

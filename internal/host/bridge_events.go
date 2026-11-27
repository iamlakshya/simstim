package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type bridgeEvents struct {
	ctx      context.Context
	stream   *link.Stream
	args     api.EventsWatchArgs
	results  chan bridgeEventResult
	sources  map[string]*bridgeEventSource
	aliases  map[string]string
	seen     map[string]uint64
	seenRoot map[string]string
}

// events owns one dedicated aggregate connection. Native discovery only asks
// for reconciliation; every emitted event comes from a real dispatcher stream
// or the exact prepared V1 record written to its existing native audit file.
func (b *bridge) events(reader *bufio.Reader, request proto.Request) (returned error) {
	refuse := func(err error) error { b.reply(request.ID, nil, err); return err }
	if err := CheckCaller(); err != nil {
		return refuse(err)
	}
	var args api.EventsWatchArgs
	if !api.ExactJSONFields(request.Args, api.EventsWatchArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.V != 1 || args.ID != "" && !api.ValidSessionID(args.ID) {
		return refuse(UserError("bad_request", "invalid event stream version or target", "Use events with a host or exact session address."))
	}
	connection, sameReader, finish, err := b.continueEventsCarrier(reader)
	if err != nil {
		return refuse(err)
	}
	transferred := false
	defer func() {
		if !transferred {
			returned = errors.Join(returned, finish())
		}
	}()
	ctx, cancel := context.WithCancel(b.ctx)
	var discovery *EventsDiscovery
	if args.Follow {
		discovery, err = OpenEventsDiscovery()
		if err != nil {
			cancel()
			return refuse(err)
		}
	}
	if err := b.w.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, OK: true}); err != nil {
		cancel()
		if discovery != nil {
			_ = discovery.Close()
		}
		return err
	}
	carrier := link.ContinueEvents(connection, sameReader, link.Supervisor)
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = carrier.Close() })
	var aggregate *bridgeEvents
	var controlJoined, discoveryJoined chan struct{}
	defer func() {
		cancel()
		if discovery != nil {
			returned = errors.Join(returned, discovery.Close())
		}
		if aggregate != nil {
			aggregate.close()
		}
		_ = carrier.Close()
		if !stop() {
			<-closed
		}
		if controlJoined != nil {
			<-controlJoined
		}
		if discoveryJoined != nil {
			<-discoveryJoined
		}
		returned = errors.Join(returned, finish())
	}()
	transferred = true
	stream, err := carrier.Open(ctx, link.KindEvents, args)
	if err != nil {
		return err
	}
	control := make(chan error, 1)
	controlJoined = make(chan struct{})
	go func() {
		defer close(controlJoined)
		var one [1]byte
		n, err := stream.Read(one[:])
		if n != 0 {
			err = UserError("protocol", "an event observer sent an input payload", "Use matching client and helper event support.")
		}
		control <- err
	}()
	wake := make(chan error, 1)
	if discovery != nil {
		discoveryJoined = make(chan struct{})
		go func() {
			defer close(discoveryJoined)
			for {
				err := discovery.Next(ctx)
				select {
				case wake <- err:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	aggregate = &bridgeEvents{ctx: ctx, stream: stream, args: args, results: make(chan bridgeEventResult, alerts.EventHistoryLimit), sources: map[string]*bridgeEventSource{}, aliases: map[string]string{}, seen: map[string]uint64{}, seenRoot: map[string]string{}}
	if err := aggregate.reconcile(); err != nil {
		return err
	}
	for {
		if !args.Follow && len(aggregate.sources) == 0 {
			_ = stream.SetWriteDeadline(time.Time{})
			return stream.Close()
		}
		select {
		case result := <-aggregate.results:
			_, err := aggregate.receive(result)
			if err != nil {
				return err
			}
		case err := <-wake:
			if err != nil {
				return err
			}
			if err := aggregate.reconcile(); err != nil {
				return err
			}
		case err := <-control:
			if err == nil || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-carrier.Done():
			return carrier.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *bridgeEvents) emit(root string, event alerts.Event) error {
	if m.args.ID != "" && event.Session.ID != m.args.ID {
		return nil
	}
	if _, err := alerts.StreamEventIdentity(event); err != nil {
		return err
	}
	if event.Sequence <= m.seen[event.Stream] {
		return nil
	}
	line := append(event.JSON(), '\n')
	if len(line) > proto.MaxFrame {
		return UserError("protocol", "one event exceeds the stream record bound", "Check the session's native audit log.")
	}
	// This is the same native output owner's ten-second per-write bound.
	if err := m.stream.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	n, err := m.stream.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err == nil {
		m.seen[event.Stream] = event.Sequence
		m.seenRoot[event.Stream] = root
	}
	return err
}

func (m *bridgeEvents) replay(root, publisher string) error {
	ctx, cancel := context.WithTimeout(m.ctx, auditPageReadBudget)
	defer cancel()
	events, err := readEventAudit(ctx, root, m.args.ID)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, event := range events {
		keep[event.Stream] = true
		if publisher != "" && event.Stream != publisher {
			continue
		}
		if err := m.emit(root, event); err != nil {
			return err
		}
	}
	for stream, owner := range m.seenRoot {
		if owner == root && !keep[stream] {
			delete(m.seenRoot, stream)
			delete(m.seen, stream)
		}
	}
	return nil
}

func (m *bridgeEvents) reconcile() error {
	ids, err := eventTargets(m.ctx, m.args.ID)
	if err != nil {
		return err
	}
	replayed := map[string]bool{}
	present := map[string]bool{}
	for _, id := range ids {
		present[id] = true
		if err := m.ctx.Err(); err != nil {
			return err
		}
		root := m.aliases[id]
		if root == "" || m.sources[root] == nil {
			source, openErr := openBridgeEventSource(m.ctx, id, m.args.ID, m.args.Follow, m.results)
			if openErr != nil {
				if !noHolderSocket(openErr) {
					return openErr
				}
				root = eventPrimaryHint(id)
				m.aliases[id] = root
			} else {
				root = source.root
				if m.sources[root] != nil {
					source.close()
				} else {
					m.sources[root] = source
				}
				m.aliases[id] = root
			}
		}
		// A live stream already owns its snapshot/future fence. Reading ahead
		// from its audit could skip older queued live events, so replay only
		// after it ended or when no native socket could be reached.
		if m.sources[root] == nil && !replayed[root] {
			if err := m.replay(root, ""); err != nil {
				return err
			}
			replayed[root] = true
		}
	}
	for id := range m.aliases {
		if !present[id] {
			delete(m.aliases, id)
		}
	}
	retained := map[string]bool{}
	for _, root := range m.aliases {
		retained[root] = true
	}
	for root := range m.sources {
		retained[root] = true
	}
	for stream, root := range m.seenRoot {
		if !retained[root] {
			delete(m.seenRoot, stream)
			delete(m.seen, stream)
		}
	}
	return nil
}

func (m *bridgeEvents) receive(result bridgeEventResult) (bool, error) {
	if result.source == nil || m.sources[result.source.root] != result.source {
		return false, nil
	}
	if result.event != nil {
		return false, m.emit(result.source.root, *result.event)
	}
	root := result.source.root
	result.source.close()
	delete(m.sources, root)
	if result.err != nil {
		if err := m.replay(root, result.source.publisher); err != nil {
			return false, err
		}
		return false, UserError("events_unavailable", "the native event stream ended before a clean final frame", "Read events again and check the session's native audit for a gap.")
	}
	return true, nil
}

func (m *bridgeEvents) close() {
	for _, source := range m.sources {
		source.close()
	}
}

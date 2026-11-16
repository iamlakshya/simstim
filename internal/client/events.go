package client

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

type eventsDialKey struct{}

func eventsDial(ctx context.Context) bool { v, _ := ctx.Value(eventsDialKey{}).(bool); return v }

// WatchEvents reads bounded V1 event records over one dedicated, verified
// ordinary bridge. Its original accepted Source survives the streaming dial.
func (c *Conn) WatchEvents(ctx context.Context, a api.EventsWatchArgs, receive func(alerts.Event) error) error {
	if a.V != 1 || a.ID != "" && !api.ValidSessionID(a.ID) || receive == nil {
		return eventsProtocol("the event stream request is invalid")
	}
	if !c.HasFeature(api.FeatureEvents) {
		return eventsUpgrade()
	}
	ctx, _ = ensureGrantRequest(ctx)
	mode := context.WithValue(context.WithValue(ctx, execDialKey{}, true), eventsDialKey{}, true)
	start, cancel := context.WithTimeout(mode, 60*time.Second)
	raw, err := dialHostContinuation(start, c)
	cancel()
	if err != nil {
		return err
	}
	defer raw.Close()
	// Pre-carrier errors still join this synchronous response-read owner.
	// On success later defers close/join the actual carrier before this mark.
	defer raw.execReadFinished()
	if !ValidHostID(c.HostID()) || raw.HostID() != c.HostID() || !sameExecTransportSource(c, raw) {
		return grantIdentityError(c.Host)
	}
	if !raw.HasFeature(api.FeatureEvents) {
		return eventsUpgrade()
	}
	locked, release, err := c.beginGrant(ctx, "events.watch", false)
	if err != nil {
		return err
	}
	defer release()
	if err := checkTransportBeforeTierMigration(locked, c); err != nil {
		return err
	}
	if err := checkTransportBeforeTierMigration(locked, raw); err != nil {
		return err
	}
	if err := c.checkGrantHost(locked, false); err != nil {
		return err
	}
	c.execBusy.Add(1)
	defer c.execBusy.Add(-1)
	stop := context.AfterFunc(ctx, raw.kill)
	defer stop()
	args, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err = raw.w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "events.watch", Args: args}); err != nil {
		return err
	}
	release()
	c.renewExecLease(ctx)
	frame, err := proto.Read(raw.execReader)
	if err != nil {
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 {
		return eventsProtocol("the helper returned an invalid event stream response")
	}
	if !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return eventsProtocol("the helper refused the event stream without an error")
	}
	stop()
	conn, reader, closeCarrier := raw.execCarrier()
	defer closeCarrier()
	carrier := link.ContinueEvents(conn, reader, link.Guest)
	defer carrier.Close()
	joined := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { defer close(joined); _ = carrier.Close() })
	defer func() {
		if !stopClose() {
			<-joined
		}
	}()
	stream, err := carrier.Accept(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	if stream.Kind() != link.KindEvents {
		return eventsProtocol("the helper opened another stream instead of events")
	}
	return readEventStream(ctx, stream, a.ID, receive)
}

func readEventStream(ctx context.Context, stream *link.Stream, id string, receive func(alerts.Event) error) error {
	if err := readEvents(ctx, stream, id, receive); err != nil {
		return err
	}
	if !stream.PeerWriteClosed() {
		return eventsProtocol("the event connection ended before its final stream frame")
	}
	return nil
}

func readEvents(ctx context.Context, input io.Reader, id string, receive func(alerts.Event) error) error {
	reader := bufio.NewReader(input)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := reader.Peek(1); err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return eventsProtocol("the event stream could not be read")
		}
		line, err := proto.ReadJSONLine(reader)
		if err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return eventsProtocol("the event stream ended with an incomplete or oversized record")
		}
		ev, err := alerts.DecodeStreamEvent(line, id)
		if err != nil {
			return eventsProtocol(err.Error())
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := receive(ev); err != nil {
			return err
		}
	}
}

func eventsProtocol(message string) error {
	return &UserError{Msg: message, Hint: "Upgrade the client and host helper, then run events again.", Code: "protocol"}
}

func eventsUpgrade() error {
	return &UserError{Msg: "the helper does not offer session events", Hint: "Upgrade the host helper, then run events again.", Code: "upgrade_required"}
}

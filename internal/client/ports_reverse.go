package client

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type reverseDialKey struct{}
func reverseDial(ctx context.Context)bool{value,_:=ctx.Value(reverseDialKey{}).(bool);return value}

// RunReverse exposes only the named laptop loopback port to one guest port.
// Its own-host gate uses the original local session source before any frame.
func (c *Conn) RunReverse(ctx context.Context, session string, laptopPort, guestPort int, onStart func()) error {
	if err := checkSessionID(c.Host.Name, session); err != nil {
		return err
	}
	if laptopPort < 1 || laptopPort > 65535 || guestPort < 1 || guestPort > 65535 {
		return &UserError{Msg: "reverse ports must be from 1 to 65535", Hint: "Name laptop-port:guest-port, or one port for both.", Code: "bad_port", Kind: KindUsage}
	}
	if c.Host.SSH == "" {
		return &UserError{Msg: "reverse forwarding needs SSH", Hint: "Choose a sandbox on an own host reached through SSH.", Code: "needs_ssh", Kind: KindRefused}
	}
	if !c.HasFeature(api.FeatureReversePortsV1) {
		return portsUpgrade()
	}
	ctx,_=ensureGrantRequest(ctx)
	base:=ctx
	start,cancel:=context.WithTimeout(context.WithValue(base,reverseDialKey{},true),60*time.Second)
	raw,err:=dialHostContinuation(start,c)
	cancel()
	if err!=nil{return err}
	defer raw.Close()
	defer raw.execReadFinished() // This caller owns the initial synchronous reader; carrier/pump defers join first.
	if !ValidHostID(c.HostID()) || raw.HostID()!=c.HostID() || !sameExecTransportSource(c,raw) || !raw.HasFeature(api.FeatureReversePortsV1){return portsUpgrade()}
	ctx, release, err := c.beginGrant(base, "reverse port forward", false)
	if err != nil {
		return err
	}
	defer release()
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return err
	}
	if creation == nil || creation.FutureLineage || creation.CreationWall == "" || creation.CreationWall == "none" {
		return &UserError{Msg: "the reverse forward has no supported local sandbox record", Hint: "Resume the sandbox through the current client first.", Code: "grant_state", Kind: KindRefused}
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		return err
	}
	if err := Allow(c.Host, tier, Outbound{Kind: OutboundReverseForward}, settings); err != nil {
		return err
	}
	if err:=checkTransportBeforeTierMigration(ctx,c);err!=nil{return err}
	if err:=checkTransportBeforeTierMigration(ctx,raw);err!=nil{return err}
	if err:=c.checkGrantHost(ctx,false);err!=nil{return err}
	c.execBusy.Add(1)
	defer c.execBusy.Add(-1)
	stop := context.AfterFunc(base, raw.kill)
	defer stop()
	args, err := json.Marshal(api.PortArgsV1{Version: 1, ID: session, Port: guestPort})
	if err != nil {
		return err
	}
	if err := raw.w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "port.reverse", Args: args}); err != nil {
		return err
	}
	release()
	c.renewExecLease(base)
	frame, err := proto.Read(raw.execReader)
	var response proto.Response
	if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 {
		return &UserError{Msg: "the helper returned an invalid reverse response", Hint: "Update the helper and resume the sandbox.", Code: "protocol", Kind: KindRefused}
	}
	if !response.OK {
		if response.Error != nil {
			return hostErrorOn(c.Host.Name, response.Error)
		}
		return portsUpgrade()
	}
	stop()
	conn, reader, closeCarrier := raw.execCarrier()
	defer closeCarrier()
	carrier := link.ContinueReverse(conn, reader, link.Guest)
	defer carrier.Close()
	if onStart != nil {
		onStart()
	}
	return c.receiveReverse(base, carrier, session, laptopPort, guestPort)
}

func (c *Conn) receiveReverse(ctx context.Context, carrier *link.Session, session string, laptopPort, guestPort int) error {
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifetime, func() { _ = carrier.Close() })
	defer stop()
	var transfers sync.WaitGroup
	defer transfers.Wait()
	limit := make(chan struct{}, 32)
	for {
		stream, err := carrier.Accept(lifetime)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &UserError{Msg: "the reverse forward connection ended", Hint: "Check the sandbox and laptop service, then forward it again.", Code: "forward_failed", Kind: KindFailed}
		}
		var header proto.PortHeaderV1
		if stream.Kind() != link.KindRPort || stream.Header(&header) != nil || !api.ExactJSONFields(stream.HeaderBytes(), struct {
			Kind link.Kind `json:"kind"`
			proto.PortHeaderV1
		}{}) || header.Version != 1 || header.Port != guestPort {
			_ = stream.Close()
			continue
		}
		select {
		case limit <- struct{}{}:
			transfers.Add(1)
			go func() {
				defer transfers.Done()
				defer func() { <-limit }()
				defer stream.Close()
				// Each physical laptop dial rechecks the SAME original transport
				// Source and the session ceiling under the actual publication OP.
				grant, release, err := c.beginGrant(lifetime, "reverse port connection", false)
				if err != nil {
					cancel()
					return
				}
				tier, creation, err := EffectiveSessionTier(grant, c.Host, session, c.HostID())
				if err != nil || creation == nil || creation.FutureLineage || creation.CreationWall == "" || creation.CreationWall == "none" {
					release()
					cancel()
					return
				}
				settings, err := ReadPolicySettings()
				if err != nil {
					release()
					cancel()
					return
				}
				if err := Allow(c.Host, tier, Outbound{Kind: OutboundReverseForward}, settings); err != nil {
					release()
					cancel()
					return
				}
				// The guest supplies no host, laptop port or proxy setting.
				connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(grant, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(laptopPort)))
				release()
				if err != nil {
					return
				}
				defer connection.Close()
				_ = link.RelayDuplex(lifetime, connection, stream)
			}()
		default:
			_ = stream.Close()
		}
	}
}

package guest

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type reverseListener struct {
	args   proto.ReversePortV1
	ctx    context.Context
	cancel context.CancelFunc
	listen net.Listener
	wg     sync.WaitGroup
	done   chan struct{}
}

func portError(message string) *proto.Error {
	return &proto.Error{Code: "port_refused", Message: message, Hint: "Choose a port from 1 to 65535 and check that the sandbox is running."}
}

func (g *Init) acceptPort(stream *link.Stream) {
	var header proto.PortHeaderV1
	if stream.Header(&header) != nil || !api.ExactJSONFields(stream.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		proto.PortHeaderV1
	}{}) || header.Version != 1 || header.Port < 1 || header.Port > 65535 {
		_ = stream.Close()
		return
	}
	g.mu.Lock()
	if g.ended || !g.sessionPrepared || g.lifetime == nil {
		g.mu.Unlock()
		_ = stream.Close()
		return
	}
	g.portWG.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.portWG.Done()
		defer stream.Close()
		ctx, cancel := context.WithCancel(g.lifetime)
		defer cancel()
		connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(header.Port)))
		if err != nil {
			return
		}
		defer connection.Close()
		_ = link.RelayDuplex(ctx, stream, connection)
	}()
}

func decodeReverse(raw []byte) (proto.ReversePortV1, error) {
	var args proto.ReversePortV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != 1 || args.Port < 1 || args.Port > 65535 || !api.ValidSessionID(args.Lease) {
		return args, portError("invalid reverse listener declaration")
	}
	return args, nil
}

func (g *Init) startReverse(raw []byte) error {
	args, err := decodeReverse(raw)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if g.ended || !g.sessionPrepared || g.lifetime == nil {
		g.mu.Unlock()
		return portError("the guest is not ready for a reverse listener")
	}
	if g.reversePorts == nil {
		g.reversePorts = make(map[string]*reverseListener)
	}
	for _, listener := range g.reversePorts {
		if listener.args.Lease == args.Lease || listener.args.Port == args.Port {
			g.mu.Unlock()
			return portError("the guest port already has a reverse listener")
		}
	}
	ctx, cancel := context.WithCancel(g.lifetime)
	listener := &reverseListener{args: args, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	g.reversePorts[args.Lease] = listener
	g.portWG.Add(1)
	g.mu.Unlock()
	connection, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(args.Port)))
	g.mu.Lock()
	if err != nil || g.ended || ctx.Err() != nil || g.reversePorts[args.Lease] != listener {
		delete(g.reversePorts, args.Lease)
		g.mu.Unlock()
		cancel()
		if connection != nil {
			_ = connection.Close()
		}
		g.portWG.Done()
		close(listener.done)
		return portError("the requested guest loopback port cannot listen")
	}
	listener.listen = connection
	g.mu.Unlock()
	go func() {
		defer g.portWG.Done()
		defer close(listener.done)
		defer listener.wg.Wait()
		defer cancel()
		stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
		defer stop()
		defer connection.Close()
		limit := make(chan struct{}, 32)
		for {
			client, err := connection.Accept()
			if err != nil {
				return
			}
			select {
			case limit <- struct{}{}:
				listener.wg.Add(1)
				go func() {
					defer listener.wg.Done()
					defer func() { <-limit }()
					defer client.Close()
					stream, err := g.sess.Open(ctx, link.KindRPort, args)
					if err != nil {
						return
					}
					defer stream.Close()
					_ = link.RelayDuplex(ctx, client, stream)
				}()
			default:
				_ = client.Close()
			}
		}
	}()
	return nil
}

func (g *Init) stopReverse(raw []byte) error {
	args, err := decodeReverse(raw)
	if err != nil {
		return err
	}
	g.mu.Lock()
	listener := g.reversePorts[args.Lease]
	if listener == nil || listener.args != args {
		g.mu.Unlock()
		return portError("no matching reverse listener is owned")
	}
	delete(g.reversePorts, args.Lease)
	g.mu.Unlock()
	listener.cancel()
	if listener.listen != nil {
		_ = listener.listen.Close()
	}
	<-listener.done
	return nil
}

func (g *Init) stopPorts() {
	g.mu.Lock()
	listeners := make([]*reverseListener, 0, len(g.reversePorts))
	for _, listener := range g.reversePorts {
		listeners = append(listeners, listener)
	}
	g.reversePorts = nil
	g.mu.Unlock()
	for _, listener := range listeners {
		listener.cancel()
		if listener.listen != nil {
			_ = listener.listen.Close()
		}
	}
	g.portWG.Wait()
}

func (g *Init) portClaims() []api.Port {
	ports := host.GuestListeningPorts()
	if len(ports) > 128 {
		ports = ports[:128]
	}
	for index := range ports {
		ports[index].Claimed = true
		ports[index].Granted, ports[index].Reverse = false, false
		ports[index].Address = api.CleanText(ports[index].Address, 64)
		ports[index].Process = api.CleanText(ports[index].Process, 64)
	}
	return ports
}

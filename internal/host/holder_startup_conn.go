package host

import (
	"bufio"
	"encoding/json"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type startupDispatchResult uint8

const (
	startupContinue startupDispatchResult = iota
	startupClose
	startupRetain
)

func (p *holderStartup) serve(conn net.Conn, hook bool) {
	defer p.serves.Done()
	c := &hclient{conn: conn, w: proto.NewWriter(conn), done: make(chan struct{})}
	retained := false
	defer func() { p.finishClient(c, retained) }()
	r, ok := p.startupHello(c, hook)
	if !ok {
		return
	}
	retained = p.serveStartupFrames(c, r)
}

func (p *holderStartup) finishClient(c *hclient, retained bool) {
	p.mu.Lock()
	delete(p.connections, c.conn)
	active := p.active
	p.mu.Unlock()
	if active != nil {
		active.drop(c)
	}
	if !retained {
		c.close()
	}
}

func (p *holderStartup) startupHello(c *hclient, hook bool) (*bufio.Reader, bool) {
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(c.conn)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return r, false
	}
	var hello proto.Hello
	if json.Unmarshal(f.Payload, &hello) != nil || hello.Check(version.Protocol) != nil {
		_ = c.w.WriteJSON(proto.TError, proto.Error{Code: "protocol", Message: "incompatible client", Hint: "Use the current client and host helper."})
		return r, false
	}
	var refused *proto.Error
	if c.access, refused = p.peer.accessOf(c.conn, hook, hello.Role); refused != nil {
		_ = c.w.WriteJSON(proto.TError, refused)
		return r, false
	}
	if err := c.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "holder", Features: holderFeatures}); err != nil {
		return r, false
	}
	return r, true
}

func (p *holderStartup) activeHolder() *Holder {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func prepareStartupDispatch(c *hclient) {
	_ = c.conn.SetDeadline(time.Time{})
	c.out = make(chan outFrame, clientQueue)
}

func (p *holderStartup) serveStartupFrames(c *hclient, r *bufio.Reader) bool {
	for {
		if active := p.activeHolder(); active != nil {
			prepareStartupDispatch(c)
			active.dispatch(c, r)
			return false
		}
		frame, err := proto.Read(r)
		if err != nil {
			return false
		}
		// Activation may have happened while Read blocked. Dispatch the
		// consumed frame once with the canonical running access policy.
		if active := p.activeHolder(); active != nil {
			prepareStartupDispatch(c)
			if active.dispatchFrame(c, frame) {
				active.dispatch(c, r)
			}
			return false
		}
		switch p.startupFrame(c, r, frame) {
		case startupClose:
			return false
		case startupRetain:
			return true
		case startupContinue:
		}
	}
}

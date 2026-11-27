package guest

import "github.com/iamlakshya/simstim/internal/proto"

// SetDNSSockets supplies pre-bound loopback UDP and TCP sockets for the guest
// init's DNS stub before CtlStart runs. If the guest init has already ended,
// socks are closed immediately.
func (g *Init) SetDNSSockets(socks DNSSockets) {
	g.mu.Lock()
	if g.ended {
		g.mu.Unlock()
		_ = socks.Close()
		return
	}
	prev := g.dnsSocks
	g.dnsSocks = socks
	g.dnsErr = nil
	g.mu.Unlock()
	_ = prev.Close()
}

func (g *Init) setInheritedDNS(socks DNSSockets, err error) {
	g.mu.Lock()
	if g.ended {
		g.mu.Unlock()
		_ = socks.Close()
		return
	}
	prev := g.dnsSocks
	g.dnsSocks = socks
	g.dnsErr = err
	g.mu.Unlock()
	_ = prev.Close()
}

func (g *Init) setupDNS(mode string) error {
	if g.vm != nil {
		return g.vm.setupDNS(mode)
	}
	g.mu.Lock()
	socks, initErr, ended := g.dnsSocks, g.dnsErr, g.ended
	g.dnsSocks = DNSSockets{}
	g.mu.Unlock()

	if ended {
		_ = socks.Close()
		return &proto.Error{Code: "stopped", Message: "the sandbox stops, so it starts no DNS stub", Hint: "Resume the session."}
	}
	switch mode {
	case "":
		_ = socks.Close()
		return nil
	case proto.DNSReal, proto.DNSSynthetic:
		if initErr != nil {
			_ = socks.Close()
			return initErr
		}
		if socks.UDP == nil || socks.TCP == nil {
			_ = socks.Close()
			return &proto.Error{
				Code:    "wall_init",
				Message: "the guest init has no inherited DNS sockets for mode " + mode,
				Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
			}
		}
		d, err := StartDNS(g.sess, socks, g.log)
		if err != nil {
			return err
		}
		g.mu.Lock()
		if g.ended {
			g.mu.Unlock()
			_ = d.Close()
			return &proto.Error{Code: "stopped", Message: "the sandbox stops, so it starts no DNS stub", Hint: "Resume the session."}
		}
		g.dns = d
		g.mu.Unlock()
		return nil
	default:
		_ = socks.Close()
		return &proto.Error{
			Code:    "bad_request",
			Message: "invalid sandbox DNS mode " + mode,
			Hint:    "Update the host helper, then resume the session.",
		}
	}
}

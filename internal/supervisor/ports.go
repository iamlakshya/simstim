package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

const portStreamOpenTimeout = 5 * time.Second

// portGrant owns one active Unix socket listener exposing one guest TCP port
// on the host while its grant holds (spec 7.4). No TCP port opens on the host.
type portGrant struct {
	port   int
	socket string
	ln     net.Listener
	done   chan struct{}

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func (g *portGrant) track(c net.Conn) bool {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		_ = c.Close()
		return false
	}
	g.conns[c] = struct{}{}
	g.mu.Unlock()
	return true
}

func (g *portGrant) untrack(c net.Conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
}

func (g *portGrant) closeAll() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	conns := g.conns
	g.conns = nil
	g.mu.Unlock()

	_ = g.ln.Close()
	_ = os.Remove(g.socket)
	for c := range conns {
		_ = c.Close()
	}
}

func (s *Supervisor) handlePortGrant(w *proto.Writer, req proto.Request) {
	var args struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(req.Args, &args) != nil || args.Port < 1 || args.Port > 65535 {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "bad_request", Message: "a TCP port between 1 and 65535 is needed", Hint: "Pass a valid port number."},
		})
		return
	}
	res, perr := s.grantPort(args.Port)
	resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
	if res != nil {
		resp.Result, _ = json.Marshal(res)
	}
	_ = w.WriteJSON(proto.TResponse, resp)
}

func (s *Supervisor) handlePortRevoke(w *proto.Writer, req proto.Request) {
	var args struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(req.Args, &args) != nil || args.Port < 1 || args.Port > 65535 {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "bad_request", Message: "a TCP port between 1 and 65535 is needed", Hint: "Pass a valid port number."},
		})
		return
	}
	revoked := s.revokePort(args.Port)
	res, _ := json.Marshal(api.PortRevokeResult{Port: args.Port, Revoked: revoked})
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
}

func (s *Supervisor) grantPort(port int) (*api.PortGrantResult, *proto.Error) {
	s.portMu.Lock()
	if s.portsClosed {
		s.portMu.Unlock()
		return nil, &proto.Error{Code: "session_gone", Message: "the session is stopping", Hint: "Start or resume the session first."}
	}
	if existing, ok := s.portGrants[port]; ok {
		sock := existing.socket
		s.portMu.Unlock()
		return &api.PortGrantResult{Port: port, Socket: sock, AlreadyGranted: true}, nil
	}
	dir := paths.PortDir(s.id)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		s.portMu.Unlock()
		return nil, &proto.Error{Code: "error", Message: err.Error(), Hint: "Check permissions on the runtime folder."}
	}
	sock := paths.PortSocket(s.id, port)
	ln, err := host.ListenPrivate(sock, "the port forward socket")
	if err != nil {
		s.portMu.Unlock()
		return nil, &proto.Error{Code: "error", Message: err.Error(), Hint: "Check the runtime folder and try again."}
	}
	first := !s.hadPortForward
	s.hadPortForward = true
	g := &portGrant{
		port:   port,
		socket: sock,
		ln:     ln,
		done:   make(chan struct{}),
		conns:  map[net.Conn]struct{}{},
	}
	s.portGrants[port] = g
	granted := s.grantedPortsLocked()
	s.wg.Add(1)
	go s.servePortGrant(g)
	s.portMu.Unlock()

	s.saveGrantedPorts(granted)
	s.audit.Record(AuditEntry{Event: "port_grant", Session: s.id, Detail: fmt.Sprintf("port %d", port)})
	return &api.PortGrantResult{Port: port, Socket: sock, FirstForward: first}, nil
}

func (s *Supervisor) revokePort(port int) bool {
	s.portMu.Lock()
	g, ok := s.portGrants[port]
	if !ok {
		s.portMu.Unlock()
		return false
	}
	delete(s.portGrants, port)
	empty := len(s.portGrants) == 0
	granted := s.grantedPortsLocked()
	s.portMu.Unlock()

	g.closeAll()
	<-g.done
	if empty {
		_ = os.Remove(paths.PortDir(s.id))
	}
	s.saveGrantedPorts(granted)
	s.audit.Record(AuditEntry{Event: "port_revoke", Session: s.id, Detail: fmt.Sprintf("port %d", port)})
	return true
}

func (s *Supervisor) closeAllPorts() {
	s.portMu.Lock()
	if s.portsClosed {
		s.portMu.Unlock()
		return
	}
	s.portsClosed = true
	grants := make([]*portGrant, 0, len(s.portGrants))
	for _, g := range s.portGrants {
		grants = append(grants, g)
	}
	s.portGrants = map[int]*portGrant{}
	s.portMu.Unlock()

	for _, g := range grants {
		g.closeAll()
		<-g.done
	}
	_ = os.RemoveAll(paths.PortDir(s.id))
}

func (s *Supervisor) servePortGrant(g *portGrant) {
	defer s.wg.Done()
	defer close(g.done)
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			return
		}
		if !s.fullPeer(conn) {
			_ = conn.Close()
			s.audit.Record(AuditEntry{Event: "port_refused", Session: s.id, Detail: fmt.Sprintf("port %d peer inside session", g.port)})
			continue
		}
		if !g.track(conn) {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer g.untrack(c)
			s.relayPortConn(g.port, c)
		}(conn)
	}
}

func (s *Supervisor) relayPortConn(port int, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), portStreamOpenTimeout)
	st, err := s.link.Open(ctx, link.KindPort, guest.PortHeader{Port: port})
	cancel()
	if err != nil {
		s.log.Warn("open guest port stream failed", "port", port, "err", err)
		return
	}
	defer st.Close()
	s.audit.Record(AuditEntry{Event: "port_connect", Session: s.id, Detail: fmt.Sprintf("port %d", port)})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyOneWay(st, conn)
	}()
	go func() {
		defer wg.Done()
		copyOneWay(conn, st)
	}()
	wg.Wait()
}

// grantedPortsLocked returns active granted ports sorted ascending. Caller holds s.portMu.
func (s *Supervisor) grantedPortsLocked() []int {
	if len(s.portGrants) == 0 {
		return nil
	}
	out := make([]int, 0, len(s.portGrants))
	for p := range s.portGrants {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func (s *Supervisor) saveGrantedPorts(ports []int) {
	s.mu.Lock()
	s.m.GrantedPorts = ports
	cp := *s.m
	s.mu.Unlock()
	if err := s.save(&cp, false); err != nil {
		s.log.Warn("manifest not saved after port grant change", "err", err)
	}
}

// mergePorts annotates guest-claimed listening ports with Claimed: true and
// merges them with the supervisor's active port grants (spec 7.4).
func (s *Supervisor) mergePorts(claimed []api.Port) []api.Port {
	byPort := make(map[int]api.Port, len(claimed))
	for _, p := range claimed {
		p.Claimed = true
		byPort[p.Port] = p
	}
	s.portMu.Lock()
	for port, g := range s.portGrants {
		entry, ok := byPort[port]
		if !ok {
			entry = api.Port{Port: port, Address: "127.0.0.1"}
		}
		entry.Granted = true
		entry.Socket = g.socket
		byPort[port] = entry
	}
	s.portMu.Unlock()

	out := make([]api.Port, 0, len(byPort))
	for _, p := range byPort {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// markAttachedWalled ensures TAttached frames from a walled session always
// carry Walled: true from the trusted supervisor, regardless of guest claims.
func markAttachedWalled(payload []byte) []byte {
	var att proto.Attached
	if json.Unmarshal(payload, &att) != nil {
		return payload
	}
	att.Walled = true
	b, err := json.Marshal(att)
	if err != nil {
		return payload
	}
	return b
}

func copyOneWay(dst io.Writer, src io.Reader) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

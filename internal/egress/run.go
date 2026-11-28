package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// EgressOpener defines the stream directions on the socket pair between the
// supervisor and the egress process: the supervisor opens ctl, egress and dns
// streams, and the egress process accepts them without listening on any port.
var EgressOpener = map[link.Kind]link.Side{
	link.KindCtl:    link.Supervisor,
	link.KindEgress: link.Supervisor,
	link.KindDNS:    link.Supervisor,
}

// StreamHeader is the JSON header carried on each KindEgress link stream.
type StreamHeader struct {
	Via  string `json:"via,omitempty"`  // "explicit" (default) or "transparent"
	Host string `json:"host,omitempty"` // transparent mode destination host or synthetic IP
	Port int    `json:"port,omitempty"` // transparent mode destination port
}

// StartArgs is sent by the supervisor on the ctl stream as the "init" request
// before any network flow is dispatched.
type StartArgs struct {
	PolicyTOML     string            `json:"policy_toml,omitempty"`
	Session        string            `json:"session,omitempty"`
	Agent          string            `json:"agent,omitempty"`
	DNSMode        DNSMode           `json:"dns_mode,omitempty"`
	ExposedSecrets map[string]string `json:"exposed_secrets,omitempty"`
}

// TargetArgs is the payload of live "policy.allow" and "policy.deny" requests.
type TargetArgs struct {
	Target string `json:"target"`
}

// ReloadArgs is the payload of a live "policy.reload" request.
type ReloadArgs struct {
	PolicyTOML string `json:"policy_toml"`
}

// StatusResult is returned by the "status" ctl request.
type StatusResult struct {
	Session  string `json:"session"`
	Dumpable int    `json:"dumpable"`
}

// Run is the entry point of the host helper's "egress" subcommand (spec 5.4).
// It hardens the process (PR_SET_DUMPABLE=0, RLIMIT_CORE=0), attaches to the
// supervisor socket pair on file descriptor 3, and serves streams until the
// socket closes. It never opens a TCP or UDP listening port.
func Run(id string) error {
	if err := hardenProcess(); err != nil {
		return err
	}
	f := os.NewFile(3, "egress-link")
	if f == nil {
		return errors.New("egress: file descriptor 3 is not open. Try: start egress from the session supervisor with a socket pair on fd 3")
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("egress: attach to socket pair on fd 3: %w. Try: pass a Unix socket pair on fd 3", err)
	}
	defer func() { _ = conn.Close() }()
	return RunOnConn(context.Background(), id, conn, nil)
}

// RunOnConn serves the egress protocol over conn (one end of a socket pair to
// the supervisor). Optional baseOpts can inject a test Resolver, Dialer or
// AddressPolicy; nil uses production defaults.
func RunOnConn(ctx context.Context, id string, conn net.Conn, baseOpts *ServerOptions) error {
	sess := link.New(conn, link.Guest, link.Options{Opener: EgressOpener})
	defer func() { _ = sess.Close() }()

	ctl, err := sess.Accept(ctx)
	if err != nil {
		return fmt.Errorf("egress: accept ctl stream: %w. Try: check that the supervisor opened the ctl stream", err)
	}
	if ctl.Kind() != link.KindCtl {
		_ = ctl.Close()
		return fmt.Errorf("egress: first stream was %q, want ctl. Try: open ctl before sending flows", ctl.Kind())
	}

	ctlReader := bufio.NewReader(ctl)
	ctlWriter := proto.NewWriter(ctl)

	srv, err := handleInit(ctlReader, ctlWriter, id, baseOpts)
	if err != nil {
		_ = ctl.Close()
		return err
	}

	return serveLink(ctx, sess, ctl, ctlReader, ctlWriter, srv, id)
}

func handleInit(r *bufio.Reader, w *proto.Writer, id string, baseOpts *ServerOptions) (*Server, error) {
	frame, err := proto.Read(r)
	if err != nil {
		return nil, fmt.Errorf("egress: read init frame: %w. Try: send an init request on the ctl stream", err)
	}
	if frame.Type != proto.TRequest {
		return nil, fmt.Errorf("egress: unexpected frame type 0x%x before init. Try: send a TRequest with op \"init\"", frame.Type)
	}
	var req proto.Request
	if err := json.Unmarshal(frame.Payload, &req); err != nil || req.Op != "init" {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "bad_init", Message: "first ctl op must be init", Hint: "send an init request first"},
		})
		return nil, errors.New("egress: first ctl op was not init. Try: send init first")
	}
	var args StartArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			_ = w.WriteJSON(proto.TResponse, proto.Response{
				ID:    req.ID,
				Error: &proto.Error{Code: "bad_init", Message: err.Error(), Hint: "send valid StartArgs JSON"},
			})
			return nil, fmt.Errorf("egress: decode init args: %w", err)
		}
	}
	cfg := Config{Session: args.Session, Agent: args.Agent}
	if args.PolicyTOML != "" {
		parsed, err := ParsePolicy([]byte(args.PolicyTOML))
		if err != nil {
			_ = w.WriteJSON(proto.TResponse, proto.Response{
				ID:    req.ID,
				Error: &proto.Error{Code: "bad_policy", Message: err.Error(), Hint: "fix the session policy TOML"},
			})
			return nil, err
		}
		cfg = parsed
		if cfg.Session == "" {
			cfg.Session = args.Session
		}
		if cfg.Agent == "" {
			cfg.Agent = args.Agent
		}
	}
	if cfg.Session == "" {
		cfg.Session = id
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "bad_policy", Message: err.Error(), Hint: "check the policy fields"},
		})
		return nil, err
	}

	opts := ServerOptions{
		Engine:   eng,
		Redactor: NewRedactor(args.ExposedSecrets),
		DNSMode:  args.DNSMode,
		Audit: func(ev AuditEvent) {
			_ = w.WriteJSON(proto.TEvent, ev)
		},
	}
	if baseOpts != nil {
		opts.Addrs = baseOpts.Addrs
		opts.Dialer = baseOpts.Dialer
		opts.SNITimeout = baseOpts.SNITimeout
		opts.NonproxyHandler = baseOpts.NonproxyHandler
		if baseOpts.Dialer != nil && baseOpts.Dialer.Engine == nil {
			baseOpts.Dialer.Engine = eng
		}
	}
	srv, err := NewServer(opts)
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "init_failed", Message: err.Error(), Hint: "check host network interfaces"},
		})
		return nil, err
	}
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true}); err != nil {
		return nil, fmt.Errorf("egress: write init response: %w", err)
	}
	return srv, nil
}

func serveLink(ctx context.Context, sess *link.Session, ctl *link.Stream, ctlReader *bufio.Reader, ctlWriter *proto.Writer, srv *Server, id string) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	lis := newStreamListener("egress-" + id)
	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 15 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return serveCtx },
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = httpSrv.Serve(lis)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		serveCtlLoop(ctlReader, ctlWriter, srv, id)
	}()

	for {
		st, err := sess.Accept(serveCtx)
		if err != nil {
			break
		}
		switch st.Kind() {
		case link.KindEgress:
			dispatchEgressStream(serveCtx, lis, srv, st, &wg)
		case link.KindDNS:
			wg.Add(1)
			go func(stream *link.Stream) {
				defer wg.Done()
				srv.DNS.ServeStream(serveCtx, stream)
			}(st)
		default:
			_ = st.Close()
		}
	}

	cancel()
	_ = lis.Close()
	_ = httpSrv.Close()
	_ = ctl.Close()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sess.Err(); err != nil && !errors.Is(err, link.ErrLinkClosed) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func dispatchEgressStream(ctx context.Context, lis *streamListener, srv *Server, st *link.Stream, wg *sync.WaitGroup) {
	var hdr StreamHeader
	if err := st.Header(&hdr); err != nil {
		_ = st.Close()
		return
	}
	if hdr.Via == "transparent" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.HandleTransparent(ctx, st, hdr.Host, hdr.Port)
		}()
		return
	}
	if err := lis.Enqueue(st); err != nil {
		_ = st.Close()
	}
}

func serveCtlLoop(r *bufio.Reader, w *proto.Writer, srv *Server, id string) {
	for {
		frame, err := proto.Read(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				_ = w.WriteJSON(proto.TError, proto.Error{Code: "ctl_read", Message: err.Error()})
			}
			return
		}
		if frame.Type != proto.TRequest {
			continue
		}
		var req proto.Request
		if err := json.Unmarshal(frame.Payload, &req); err != nil {
			continue
		}
		_ = w.WriteJSON(proto.TResponse, handleCtlRequest(req, srv, id))
	}
}

func handleCtlRequest(req proto.Request, srv *Server, id string) proto.Response {
	switch req.Op {
	case "policy.allow":
		var a TargetArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_args", Message: err.Error(), Hint: "pass target"}}
		}
		if err := srv.Engine.Allow(a.Target); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "policy_locked", Message: err.Error(), Hint: "remove any matching lock first"}}
		}
		return proto.Response{ID: req.ID, OK: true}
	case "policy.deny":
		var a TargetArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_args", Message: err.Error(), Hint: "pass target"}}
		}
		if err := srv.Engine.Deny(a.Target); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_rule", Message: err.Error(), Hint: "pass a valid host, host:port or CIDR"}}
		}
		return proto.Response{ID: req.ID, OK: true}
	case "policy.reload":
		var a ReloadArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_args", Message: err.Error(), Hint: "pass policy_toml"}}
		}
		cfg, err := ParsePolicy([]byte(a.PolicyTOML))
		if err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_policy", Message: err.Error(), Hint: "fix the policy TOML"}}
		}
		if err := srv.Engine.Reload(cfg); err != nil {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "bad_policy", Message: err.Error(), Hint: "fix the policy fields"}}
		}
		return proto.Response{ID: req.ID, OK: true}
	case "status":
		d, _ := processDumpable()
		res, _ := json.Marshal(StatusResult{Session: id, Dumpable: d})
		return proto.Response{ID: req.ID, OK: true, Result: res}
	default:
		return proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: "unknown_op", Message: fmt.Sprintf("unknown ctl op %q", req.Op), Hint: "use policy.allow, policy.deny, policy.reload or status"},
		}
	}
}

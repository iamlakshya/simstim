package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall"
)

// ShellOptions configures opening a shell session inside a session's sandbox
// (or on the host for unwalled sessions). Shared between `shell` CLI and the
// launcher's `session: shell` action (cli-spec 15.2).
type ShellOptions struct {
	New      bool
	Name     string
	Cols     int
	Rows     int
	Settings Settings
}

// FindShellSession returns the newest alive child shell session in target's
// sandbox, or false if none is running (isolation spec 6.11). Matching only
// child sessions (SandboxOf == rootID) avoids mistaking a primary session
// started with a custom command (-- <cmd>) for an interactive sandbox shell.
func FindShellSession(list []Session, target Session) (Session, bool) {
	rootID := target.SandboxOf
	if rootID == "" {
		rootID = target.ID
	}
	var best Session
	found := false
	for _, s := range list {
		if s.Agent != "shell" || !s.Alive || Ended(s) || s.SandboxOf != rootID {
			continue
		}
		if !found || s.CreatedAt.After(best.CreatedAt) {
			best = s
			found = true
		}
	}
	return best, found
}

// OpenShellSession finds an existing running shell in target's sandbox (unless
// opts.New is true) or spawns a new one inside the sandbox. For an unwalled
// session (level none), it opens a normal host shell session in target.Cwd
// (isolation spec 6.11). It returns the session and whether a new session was
// started.
func OpenShellSession(ctx context.Context, conn *Conn, target Session, opts ShellOptions) (*Session, bool, error) {
	if opts.Name != "" {
		if err := CheckSessionName(opts.Name); err != nil {
			return nil, false, err
		}
	}
	settings := opts.Settings
	if settings.Attach.DetachKey == "" {
		settings = LoadSettings()
	}
	if !api.IsWalled(target.Wall) {
		args := UnwalledShellArgs(conn.Host, target, ShellOptions{
			Name:     opts.Name,
			Cols:     opts.Cols,
			Rows:     opts.Rows,
			Settings: settings,
		})
		s, err := conn.NewSession(ctx, args)
		return s, err == nil, err
	}
	if !opts.New {
		list, err := conn.Sessions(ctx)
		if err != nil {
			return nil, false, err
		}
		if existing, ok := FindShellSession(list, target); ok {
			return &existing, false, nil
		}
	}
	if !conn.HasFeature("spawn") {
		return nil, false, &UserError{
			Msg:  "the helper on " + conn.Host.Name + " is too old for shells and commands in a sandbox",
			Hint: brand.Name + " host add " + conn.Host.Name + " (updates the helper)",
			Code: "wall_helper_old",
		}
	}
	args := SessionArgs(NewArgs{
		In:    target.ID,
		Agent: "shell",
		Name:  opts.Name,
		Shell: settings.Sandbox.Shell,
	}, opts.Cols, opts.Rows, settings)
	s, err := conn.NewSession(ctx, args)
	return s, err == nil, err
}

// UnwalledShellArgs builds NewArgs for opening a host shell beside an unwalled
// session, carrying Wall="none", Downgrade=true, and the host/session floor
// metadata so G9 records the downgrade, shows !, and emits wall_downgraded.
func UnwalledShellArgs(h Host, target Session, opts ShellOptions) NewArgs {
	floor, from := "", ""
	if target.Wall != nil {
		floor = strings.TrimSpace(target.Wall.Floor)
		from = strings.TrimSpace(target.Wall.DowngradedFrom)
	}
	if floor == "" || from == "" {
		df, dfrom := DowngradeForNone(h, wall.None)
		if floor == "" {
			floor = df
		}
		if from == "" {
			from = dfrom
		}
	}
	return SessionArgs(NewArgs{
		Agent:          "shell",
		Cwd:            target.Cwd,
		Name:           opts.Name,
		Wall:           api.LevelNone,
		Downgrade:      true,
		WallFloor:      floor,
		DowngradedFrom: from,
	}, opts.Cols, opts.Rows, opts.Settings)
}

// OpenStreamOp opens a dedicated bridge connection on h for a streaming bridge
// op (such as "exec", "sftp" or "port.reverse" per isolation spec 10.5). After
// receiving an OK TResponse, it switches the connection to an internal/link
// session (Guest side with BridgeOpener and AllowVersionMismatch) and returns
// the session plus a cleanup function that tears down the link and process.
func OpenStreamOp(ctx context.Context, h Host, interactive bool, op, requiredFeature string, args, respOut any) (*link.Session, func(), error) {
	if !helpers.Embedded() {
		helpers.LoadCached(version.Version, CacheDir())
	}
	rt, err := routeTo(ctx, h, interactive)
	if err != nil {
		return nil, nil, err
	}
	remote := bridgeCommand(HelperVersion(), 0, helperDigests())
	proc, err := rt.t.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, nil, err
	}
	stdin := proc.Stdin()
	w := proto.NewWriter(stdin)
	r := bufio.NewReaderSize(proc.Stdout(), 256<<10)

	abort := func() {
		_ = stdin.Close()
		proc.Kill()
		_ = proc.Wait()
	}

	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "client",
	}); err != nil {
		abort()
		return nil, nil, err
	}

	helloF, err := readFrameCtx(ctx, r)
	if err != nil {
		abort()
		return nil, nil, streamStartError(h, proc, rt.t.ReachHint(), err)
	}
	if helloF.Type != proto.THello {
		abort()
		return nil, nil, errors.New("unexpected first frame from host")
	}
	var hello proto.Hello
	if err := json.Unmarshal(helloF.Payload, &hello); err != nil {
		abort()
		return nil, nil, fmt.Errorf("read the host's hello: %w", err)
	}
	if err := hello.Check(version.Protocol); err != nil {
		abort()
		return nil, nil, &UserError{Msg: CleanLine(err.Error()), Hint: brand.Name + " host add " + h.Name + " (reinstalls the helper)", Code: "protocol"}
	}
	if requiredFeature != "" && !slices.Contains(hello.Features, requiredFeature) {
		abort()
		return nil, nil, &UserError{
			Msg:  "the helper on " + h.Name + " is too old for " + op,
			Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
			Code: "wall_helper_old",
		}
	}

	req := proto.Request{ID: 1, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			abort()
			return nil, nil, fmt.Errorf("encode %s request: %w", op, err)
		}
		req.Args = b
	}
	if err := w.WriteJSONCtx(ctx, proto.TRequest, req); err != nil {
		abort()
		return nil, nil, err
	}
	respF, err := readFrameCtx(ctx, r)
	if err != nil {
		abort()
		return nil, nil, err
	}
	if respF.Type != proto.TResponse {
		abort()
		return nil, nil, fmt.Errorf("unexpected response frame %#02x for %s", byte(respF.Type), op)
	}
	var resp proto.Response
	if err := json.Unmarshal(respF.Payload, &resp); err != nil {
		abort()
		return nil, nil, fmt.Errorf("decode %s response: %w", op, err)
	}
	if !resp.OK {
		abort()
		if resp.Error != nil {
			return nil, nil, hostErrorOn(h.Name, resp.Error)
		}
		return nil, nil, errors.New("the host refused " + op)
	}
	if respOut != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, respOut); err != nil {
			abort()
			return nil, nil, fmt.Errorf("decode %s result: %w", op, err)
		}
	}

	pconn := newProcStreamConn(proc, r, stdin)
	ls := link.New(pconn, link.Guest, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
	})
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = ls.Close()
			_ = pconn.Close()
		})
	}
	return ls, cleanup, nil
}

// Exec runs a one-shot non-interactive command inside a walled session on h
// over a dedicated streaming bridge connection (isolation spec 6.11, 10.2,
// 10.5). It demultiplexes stdout and stderr into the given writers and returns
// the command's exit code (or 125 on pre-command / stream failures).
func Exec(ctx context.Context, h Host, args api.ExecArgs, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	var resp api.ExecResponse
	dialCtx, dialCancel := context.WithTimeout(ctx, 60*time.Second)
	ls, cleanup, err := OpenStreamOp(dialCtx, h, false, "exec", "spawn", args, &resp)
	dialCancel()
	if err != nil {
		return 125, err
	}
	defer cleanup()

	acceptCtx, acceptCancel := context.WithTimeout(ctx, 15*time.Second)
	st, err := ls.Accept(acceptCtx)
	acceptCancel()
	if err != nil {
		return 125, fmt.Errorf("accept exec stream: %w", err)
	}
	defer func() { _ = st.Close() }()

	doneCh := make(chan struct{})
	defer close(doneCh)
	// When ctx cancels (e.g. Ctrl+C), close the stream both ways immediately
	// so the supervisor closes the guest stream and kills the command.
	go func() {
		select {
		case <-ctx.Done():
			_ = st.Close()
			cleanup()
		case <-doneCh:
		}
	}()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(st, stdin)
			_ = st.CloseWrite()
		}()
	} else {
		_ = st.CloseWrite()
	}

	code, err := api.ReadExecStream(st, stdout, stderr)
	if err != nil {
		if ctx.Err() != nil {
			return 130, ctx.Err()
		}
		return 125, err
	}
	return code, nil
}

func readFrameCtx(ctx context.Context, r *bufio.Reader) (proto.Frame, error) {
	type res struct {
		f   proto.Frame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f, err := proto.Read(r)
		ch <- res{f, err}
	}()
	select {
	case <-ctx.Done():
		return proto.Frame{}, ctx.Err()
	case x := <-ch:
		return x.f, x.err
	}
}

func streamStartError(h Host, proc Proc, hint string, err error) error {
	raw := strings.TrimSpace(proc.Stderr())
	if raw == "" {
		return err
	}
	if ke := hostKeyChangedError(h, raw); ke != nil {
		return ke
	}
	return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", h.Name, tailStderr(raw)), Hint: hint, Code: "unreachable"}
}

type procStreamConn struct {
	proc   Proc
	r      *bufio.Reader
	w      io.WriteCloser
	once   sync.Once
	closed chan struct{}
}

func newProcStreamConn(proc Proc, r *bufio.Reader, w io.WriteCloser) *procStreamConn {
	return &procStreamConn{proc: proc, r: r, w: w, closed: make(chan struct{})}
}

func (p *procStreamConn) Read(b []byte) (int, error)  { return p.r.Read(b) }  //nolint:wrapcheck // net.Conn io.Reader pass-through
func (p *procStreamConn) Write(b []byte) (int, error) { return p.w.Write(b) } //nolint:wrapcheck // net.Conn io.Writer pass-through
func (p *procStreamConn) Close() error {
	p.once.Do(func() {
		close(p.closed)
		_ = p.w.Close()
		done := make(chan struct{})
		go func() {
			_ = p.proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			p.proc.Kill()
			<-done
		}
	})
	return nil
}
func (p *procStreamConn) LocalAddr() net.Addr              { return procAddr("client") }
func (p *procStreamConn) RemoteAddr() net.Addr             { return procAddr("bridge") }
func (p *procStreamConn) SetDeadline(time.Time) error      { return nil }
func (p *procStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (p *procStreamConn) SetWriteDeadline(time.Time) error { return nil }

type procAddr string

func (a procAddr) Network() string { return "stdio" }
func (a procAddr) String() string  { return string(a) }

package guest

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// RunEditorSFTPV1 is a fixed guest helper command. Only the child created by
// agentCommand/startOwned reaches this body as agent; init never serves paths.
func RunEditorSFTPV1() error {
	if os.Geteuid() != AgentUID || os.Getegid() != AgentGID {
		return execError("the SFTP helper requires the guest agent account")
	}
	transport := &editorSFTPStdioV1{os.Stdin, os.Stdout}
	server, err := sftp.NewServer(transport)
	if err != nil {
		return err
	}
	defer server.Close()
	err = server.Serve()
	if err == io.EOF {
		return nil
	}
	return err
}

type editorSFTPStdioV1 struct{ input, output *os.File }

func (s *editorSFTPStdioV1) Read(data []byte) (int, error)  { return s.input.Read(data) }
func (s *editorSFTPStdioV1) Write(data []byte) (int, error) { return s.output.Write(data) }
func (s *editorSFTPStdioV1) Close() error                   { return s.input.Close() }

// prepareEditorV1 runs only on init's existing supervisor control stream. It
// retains the actual prepared member and never imports client environment.
func (g *Init) prepareEditorV1(raw []byte) error {
	var args proto.CtlEditorPrepareArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.ID) || !api.ValidSessionID(args.Session) || args.Run == "" {
		return execError("invalid owned editor operation")
	}
	g.mu.Lock()
	member := g.preparedMembers[args.Session]
	valid := !g.ended && g.lifetime != nil && g.lifetime.Err() == nil && member != nil && member.state == memberConsumed && member.projected.RunToken == args.Run && g.members[args.Session] != nil
	var env []string
	var cwd string
	if valid {
		env = append([]string(nil), member.projected.Env...)
		cwd = member.projected.Cwd
	}
	parent := g.lifetime
	g.mu.Unlock()
	if !valid {
		return execError("the original guest member is no longer owned")
	}
	if g.vm != nil {
		env = g.vm.childEnvironment(env)
	}
	argv := []string{"/bin/sh", "-l"}
	kind := link.KindHolder
	switch args.Purpose {
	case proto.EditorExecV1:
		if args.Command == "" || len(args.Command) > proto.EditorSSHCommandMaxV1 {
			return execError("invalid guest shell command")
		}
		argv = []string{"/bin/sh", "-c", args.Command}
		kind = link.KindExec
	case proto.EditorTerminalV1:
		shell := proto.CtlStartArgs{DefaultShell: true, Argv: argv}
		if err := g.resolveShell(&shell); err != nil {
			return err
		}
		argv = shell.Argv
		if args.Command != "" {
			if len(args.Command) > proto.EditorSSHCommandMaxV1 {
				return execError("the guest shell command is too large")
			}
			argv = []string{argv[0], "-c", args.Command}
		}
	case proto.EditorSFTPV1:
		if args.Command != "" {
			return execError("SFTP takes no shell command")
		}
		helper, err := g.helperExecutable()
		if err != nil {
			return err
		}
		argv = []string{helper, "wall-sftp-v1"}
		kind = link.KindSFTP
	default:
		return execError("unsupported editor operation")
	}
	_, err := g.registerCommand(parent, proto.CtlExecArgs{ID: args.ID, Argv: argv, Cwd: cwd}, env, kind, args.Purpose, args.Purpose == proto.EditorExecV1)
	return err
}

// Cancellation names only an operation already issued by the sole trusted
// control owner. ACK follows its actual owned child and IO joins.
func (g *Init) cancelEditorV1(raw []byte) error {
	var args proto.CtlEditorCancelArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.ID) {
		return execError("invalid editor cancellation")
	}
	g.mu.Lock()
	run := g.execs[args.ID]
	if run != nil && run.purpose != proto.EditorExecV1 && run.purpose != proto.EditorTerminalV1 && run.purpose != proto.EditorSFTPV1 {
		g.mu.Unlock()
		return execError("the command is not an editor operation")
	}
	if run != nil {
		run.cancel()
		if run.stream != nil {
			_ = run.stream.SetDeadline(time.Now())
		}
	}
	g.mu.Unlock()
	if run != nil {
		<-run.done
	}
	return nil
}

func (g *Init) takeEditorV1(st *link.Stream, purpose string) (*execRun, proto.EditorGuestHeaderV1, error) {
	var header proto.EditorGuestHeaderV1
	if !api.ExactJSONFields(st.HeaderBytes(), header) || st.Header(&header) != nil || header.Version != 1 || header.Purpose != purpose {
		return nil, header, execError("invalid editor stream")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	run := g.execs[header.ID]
	if g.ended || run == nil || run.taken || run.kind != st.Kind() || run.purpose != purpose || run.ctx.Err() != nil {
		return nil, header, execError("the editor operation is absent or already consumed")
	}
	run.taken, run.stream = true, st
	return run, header, nil
}

func (g *Init) serveEditorSFTPV1(st *link.Stream) {
	run, _, err := g.takeEditorV1(st, proto.EditorSFTPV1)
	if err != nil {
		_ = st.Close()
		return
	}
	defer func() { _ = st.Close(); g.finishCommand(run) }()
	// The fixed executable runs under commandProcess's agent credentials and
	// joins through the same startOwned/waitOwned path as normal guest exec.
	_ = g.executeOwned(run, ownedCommandIO{Out: st, Err: io.Discard,
		AbortOutput: func() { _ = st.SetWriteDeadline(time.Now()) },
		AttachInput: func(input io.WriteCloser) func() {
			done := make(chan struct{})
			if err := proto.NewWriter(st).WriteJSON(proto.TResponse, proto.EditorReadyV1{Version: 1, Ready: true}); err != nil {
				_ = input.Close()
				run.cancel()
				close(done)
				return func() {}
			}
			go func() {
				defer close(done)
				_, err := io.Copy(input, st)
				_ = input.Close()
				if err != nil || !st.PeerWriteClosed() {
					run.cancel()
				}
			}()
			return func() { _ = st.SetReadDeadline(time.Now()); <-done }
		},
	})
	_ = st.CloseWrite()
}

func (g *Init) tryEditorTerminalV1(st *link.Stream) bool {
	var header struct {
		Purpose string `json:"purpose"`
	}
	if st.Header(&header) != nil || header.Purpose != proto.EditorTerminalV1 {
		return false
	}
	go g.serveEditorTerminalV1(st)
	return true
}

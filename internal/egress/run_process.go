package egress

import (
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Run is the `<helper> egress <id>` entrypoint. It marks the process
// non-dumpable first, then serves the supervisor's link on fd 3.
func Run(id string) error {
	return RunWithOptions(id, Options{})
}

// RunWithOptions runs the egress process on fd 3 with custom Options.
func RunWithOptions(id string, opt Options) error {
	if err := setUndumpable(); err != nil {
		return &proto.Error{
			Code:    "egress_dumpable",
			Message: "cannot mark the egress process non-dumpable: " + err.Error(),
			Hint:    "Check the host kernel and container permissions, then resume the session.",
		}
	}
	if !api.ValidSessionID(id) {
		return &proto.Error{
			Code:    "bad_request",
			Message: "egress requires a valid session ID",
			Hint:    "Only a supervisor starts the egress process, for a session in a sandbox.",
		}
	}
	f := os.NewFile(linkFD, "egress-link")
	if f == nil {
		return &proto.Error{
			Code:    "egress_link",
			Message: "the egress process has no link on fd 3",
			Hint:    "Only a supervisor starts the egress process, with its link on fd 3.",
		}
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return &proto.Error{
			Code:    "egress_link",
			Message: "the egress process cannot use its link on fd 3: " + err.Error(),
			Hint:    "Only a supervisor starts the egress process, with its link on fd 3.",
		}
	}
	signal.Ignore(syscall.SIGPIPE)
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	srv, err := NewServer(conn, opt)
	if err != nil {
		_ = conn.Close()
		return &proto.Error{
			Code:    "egress_init",
			Message: "cannot start the egress server: " + err.Error(),
			Hint:    "Read the session's supervisor.log, then resume the session.",
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			_ = srv.Close()
		case <-srv.Done():
		}
	}()
	srv.Serve()
	return nil
}

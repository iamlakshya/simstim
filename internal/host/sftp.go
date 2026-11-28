package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
)

// stdioRWC adapts a separate reader and writer (stdin and stdout) into an
// io.ReadWriteCloser for pkg/sftp.NewServer.
type stdioRWC struct {
	io.Reader
	io.Writer
}

func (stdioRWC) Close() error { return nil }

// RunSFTP serves SFTP over in and out for session id, with the session's
// workspace (m.Cwd) as the working directory. It uses github.com/pkg/sftp so
// that scp, sftp, editors and `cp` all share one standard SFTP path.
func RunSFTP(id string, in io.Reader, out io.Writer) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if !validID(id) {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	m, err := LoadManifest(id)
	if err != nil {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	workDir := m.Cwd
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	if st, err := os.Stat(workDir); err != nil || !st.IsDir() {
		return UserError("cwd_missing", fmt.Sprintf("the workspace %s of session %s is missing", workDir, id), "Check the folder on the host.")
	}
	srv, err := sftp.NewServer(stdioRWC{Reader: in, Writer: out}, sftp.WithServerWorkingDirectory(workDir))
	if err != nil {
		return fmt.Errorf("start SFTP server: %w", err)
	}
	defer srv.Close()
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve SFTP: %w", err)
	}
	return nil
}

// RunDialPort dials a loopback TCP port on this host for session id, writes a
// one-line JSON status reply, and splices in/out with the connection. Only
// loopback addresses (127.0.0.1, ::1, localhost) are allowed.
func RunDialPort(args []string, in io.Reader, out io.Writer) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if len(args) != 3 {
		return UserError("usage", "usage: dial-port <session-id> <host> <port>", "Pass a session ID, loopback host and port.")
	}
	id, rawHost, rawPort := args[0], args[1], args[2]
	writeReply := func(rep api.DialPortReply) error {
		b, _ := json.Marshal(rep)
		if _, err := out.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("write dial-port reply: %w", err)
		}
		return nil
	}
	if !validID(id) {
		_ = writeReply(api.DialPortReply{Error: "no session " + id})
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if _, err := LoadManifest(id); err != nil {
		_ = writeReply(api.DialPortReply{Error: "no session " + id})
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	dialHost, err := loopbackHost(rawHost)
	if err != nil {
		_ = writeReply(api.DialPortReply{Error: err.Error()})
		return UserError("forbidden", err.Error(), "Only loopback addresses (127.0.0.1, ::1, localhost) may be forwarded.")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		_ = writeReply(api.DialPortReply{Error: "invalid port " + rawPort})
		return UserError("bad_port", "invalid port "+rawPort, "Pass a TCP port from 1 to 65535.")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(dialHost, strconv.Itoa(port)))
	if err != nil {
		_ = writeReply(api.DialPortReply{Error: err.Error()})
		return fmt.Errorf("dial %s:%d: %w", dialHost, port, err)
	}
	defer conn.Close()
	if err := writeReply(api.DialPortReply{OK: true}); err != nil {
		return err
	}
	spliceTCP(conn, in, out)
	return nil
}

func loopbackHost(h string) (string, error) {
	switch h {
	case "127.0.0.1", "localhost":
		return "127.0.0.1", nil
	case "::1":
		return "::1", nil
	default:
		return "", fmt.Errorf("address %q is not loopback", h)
	}
}

func spliceTCP(conn net.Conn, in io.Reader, out io.Writer) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, in)
		if tc, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	_, _ = io.Copy(out, conn)
	_ = conn.Close()
	wg.Wait()
}

package guest

import (
	"bufio"
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/memberenv"
)

// SSHProxyHelperMode is the fixed stdio transport used by guest OpenSSH.
const SSHProxyHelperMode = memberenv.SSHProxyHelperMode

// RunSSHProxyHelper connects only to the guest's fixed loopback proxy. The
// existing egress engine still decides the requested destination and port.
// Stdin is owned by this short-lived child and may be closed to join cleanup.
func RunSSHProxyHelper(args []string) error {
	if runtime.GOOS != "linux" || os.Geteuid() != AgentUID || os.Getegid() != AgentGID {
		return execError("the SSH proxy transport requires the fixed Linux guest account")
	}
	target, err := sshProxyTarget(args)
	if err != nil {
		return err
	}
	proxy, err := url.Parse(host.DefaultGuestProxy)
	if err != nil || proxy.Scheme != "http" || proxy.Host != "127.0.0.1:3128" {
		return execError("the fixed guest SSH proxy is unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", proxy.Host)
	if err != nil {
		return execError("the guest SSH proxy cannot be reached")
	}
	defer connection.Close()
	return relaySSHProxy(ctx, target, connection, os.Stdin, os.Stdout)
}

func sshProxyTarget(args []string) (string, error) {
	if len(args) != 2 || args[0] == "" || len(args[0]) > 253 || strings.ContainsAny(args[0], "\r\n\x00 /\\?#@") {
		return "", execError("the SSH proxy needs one host and decimal port")
	}
	port, err := strconv.Atoi(args[1])
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != args[1] {
		return "", execError("the SSH proxy port must be from 1 to 65535")
	}
	target := net.JoinHostPort(args[0], args[1])
	if _, err := http.NewRequest(http.MethodConnect, "http://"+target, nil); err != nil {
		return "", execError("the SSH proxy host is invalid")
	}
	return target, nil
}

func relaySSHProxy(ctx context.Context, target string, connection net.Conn, input io.ReadCloser, output io.Writer) (returned error) {
	defer func() {
		if ctx.Err() != nil {
			returned = ctx.Err()
		}
	}()
	defer connection.Close()
	defer input.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close(); _ = input.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	request, err := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return execError("the SSH proxy target is invalid")
	}
	if err := request.Write(connection); err != nil {
		return execError("the guest SSH proxy request failed")
	}
	limited := &io.LimitedReader{R: connection, N: 16 << 10}
	reader := bufio.NewReaderSize(limited, 4096)
	response, err := http.ReadResponse(reader, request)
	if err != nil || response.StatusCode != http.StatusOK {
		return execError("the network policy refused the SSH connection")
	}
	// The header cap ends here. Keep buffered tunnel bytes, including a server
	// banner sent in the same packet as the successful CONNECT response.
	limited.N = math.MaxInt64
	_ = connection.SetDeadline(time.Time{})
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		if half, ok := connection.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		done <- err
	}()
	_, readErr := io.Copy(output, reader)
	_ = connection.Close()
	_ = input.Close()
	writeErr := <-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil || writeErr != nil {
		return execError("the SSH proxy connection ended before its stream completed")
	}
	return nil
}

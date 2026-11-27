package guest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func sshProxyPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	first, file, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	second, err := net.FileConn(file)
	file.Close()
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close(); second.Close() })
	return first, second
}

func TestGuestSSHProxyPreservesBufferedBannerAndInputHalfClose(t *testing.T) {
	client, proxy := sshProxyPair(t)
	input := []byte("client SSH bytes\x00\xff")
	banner := []byte("SSH-2.0-fixture\r\n\x00\xff")
	seen := make(chan error, 1)
	go func() {
		defer proxy.Close()
		reader := bufio.NewReader(proxy)
		request, err := http.ReadRequest(reader)
		if err != nil {
			seen <- err
			return
		}
		if request.Method != http.MethodConnect || request.Host != "code.example.test:22" {
			seen <- errors.New("wrong CONNECT destination")
			return
		}
		_, err = proxy.Write(append([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"), banner...))
		if err != nil {
			seen <- err
			return
		}
		got, err := io.ReadAll(reader)
		if err == nil && !bytes.Equal(got, input) {
			err = errors.New("SSH client bytes changed")
		}
		seen <- err
	}()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := relaySSHProxy(ctx, "code.example.test:22", client, io.NopCloser(bytes.NewReader(input)), &output); err != nil {
		t.Fatal(err)
	}
	if err := <-seen; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), banner) {
		t.Fatal("buffered SSH banner changed or was lost")
	}
}

func TestGuestSSHProxyCancellationClosesAndJoinsBlockedInput(t *testing.T) {
	client, proxy := sshProxyPair(t)
	input, writer := io.Pipe()
	defer writer.Close()
	ready := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		reader := bufio.NewReader(proxy)
		_, _ = http.ReadRequest(reader)
		_, _ = io.WriteString(proxy, "HTTP/1.1 200 Connection Established\r\n\r\n")
		close(ready)
		_, _ = io.Copy(io.Discard, reader)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- relaySSHProxy(ctx, "code.example.test:22", client, input, io.Discard) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy setup did not complete")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel was lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not join the blocked input reader")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy peer retained a cancelled connection")
	}
}

func TestGuestSSHProxyRejectsInvalidTargetsBeforeDial(t *testing.T) {
	for _, args := range [][]string{nil, {"code.example.test"}, {"code.example.test", "0"}, {"code.example.test", "65536"}, {"code.example.test", "+22"}, {"code.example.test", "022"}, {"code.example.test\r\nInjected: yes", "22"}, {"user@code.example.com", "22"}, {"code.example.test/path", "22"}} {
		if _, err := sshProxyTarget(args); err == nil {
			t.Fatal("invalid CONNECT target accepted")
		}
	}
	if target, err := sshProxyTarget([]string{"code.example.test", "22"}); err != nil || target != "code.example.test:22" {
		t.Fatal("valid target refused", err)
	}
}

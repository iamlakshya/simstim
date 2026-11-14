//go:build darwin || linux

package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// This opt-in fixture runs only in an admitted, isolated local test job.
// The server uses a newly generated key and none authentication. Neither
// side can reach a real agent or read the user's SSH configuration.
func TestNativeCollectorRealOpenSSH(t *testing.T) {
	if os.Getenv("H5_NATIVE_AGENT") != "1" {
		t.Skip("set H5_NATIVE_AGENT=1 in the isolated local native-SSH validation job")
	}
	agentClientEnv(t)
	for _, certificate := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "certificate"}[certificate], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(private)
			if err != nil {
				t.Fatal(err)
			}
			if certificate {
				cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{"127.0.0.1"}, ValidBefore: ssh.CertTimeInfinity}
				if err := cert.SignCert(rand.Reader, signer); err != nil {
					t.Fatal(err)
				}
				signer, err = ssh.NewCertSigner(cert, signer)
				if err != nil {
					t.Fatal(err)
				}
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(signer)
			serverResult := make(chan error, 1)
			go nativeCollectorServer(ctx, listener, config, serverResult)
			lease, err := agentbridge.NewLease()
			if err != nil {
				t.Fatal(err)
			}
			collector, err := newAgentBindingCollector(ctx, agentbridge.Scope{Session: "abcd2345", Lease: lease})
			if err != nil {
				t.Fatal(err)
			}
			defer collector.Close()
			command := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-vv", "-E", collector.log.Path(),
				"-o", "ForwardAgent="+collector.socket.path, "-o", "BatchMode=yes", "-o", "PreferredAuthentications=none",
				"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "GlobalKnownHostsFile=/dev/null",
				"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "FingerprintHash=sha256", "-o", "HostKeyAlgorithms="+signer.PublicKey().Type(),
				"-p", fmt.Sprint(listener.Addr().(*net.TCPAddr).Port), "-T", "--", "test@127.0.0.1", "true")
			command.WaitDelay = sshWaitDelay
			program, err := openNativeAgentProgram(command.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer program.Close()
			process, err := CommandProc(command, nil)
			if err != nil {
				t.Fatal(err)
			}
			owner := &sshProc{Proc: process, rawDone: make(chan struct{}), ctx: ctx, logFile: &sshLogFile{agent: collector.log}}
			defer func() { owner.Kill(); _ = owner.Wait() }()
			if err := collector.attachOwner(owner, ctx, program); err != nil {
				t.Fatal(err)
			}
			if err := collector.wait(ctx); err != nil {
				t.Fatal(err)
			}
			collector.mu.Lock()
			record := append([]byte(nil), collector.record...)
			collector.mu.Unlock()
			key := signer.PublicKey()
			if cert, ok := key.(*ssh.Certificate); ok {
				key = cert.Key
			}
			if err := verifyNativeAgentRecord(record, agentNativePeer{keyType: signer.PublicKey().Type(), fingerprint: ssh.FingerprintSHA256(key), certificate: certificate}); err != nil {
				t.Fatalf("the actual native first hook did not bind the negotiated fixture key: %v", err)
			}
			select {
			case err := <-serverResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func nativeCollectorServer(ctx context.Context, listener net.Listener, config *ssh.ServerConfig, result chan<- error) {
	connection, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		result <- err
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.UnknownChannelType, "fixture expects a session")
			continue
		}
		session, messages, err := channel.Accept()
		if err != nil {
			result <- err
			return
		}
		defer session.Close()
		forwarded := false
		for request := range messages {
			switch request.Type {
			case "auth-agent-req@openssh.com":
				forwarded = true
				_ = request.Reply(true, nil)
			case "exec":
				if !forwarded {
					result <- fmt.Errorf("the native client omitted its forwarding request")
					return
				}
				_ = request.Reply(true, nil)
				agentChannel, incoming, err := server.OpenChannel("auth-agent@openssh.com", nil)
				if err != nil {
					result <- err
					return
				}
				go ssh.DiscardRequests(incoming)
				// The collector closes immediately after its native-only ACK.
				// Any later host bytes have no local endpoint to reach.
				_, err = io.Copy(io.Discard, agentChannel)
				_ = agentChannel.Close()
				result <- err
				return
			default:
				_ = request.Reply(false, nil)
			}
		}
	}
	result <- io.EOF
}

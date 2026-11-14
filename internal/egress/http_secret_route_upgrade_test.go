package egress

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPSecretRouteClassicWebSocketScrubsLogicalEchoAndRejectsClientStandIn(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const accept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	var sentinel string
	firstRead := make(chan error, 1)
	lastRead := make(chan error, 1)
	f := newSecretRouteFixture(t, 1, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The checked transport offers both protocols. Classic Upgrade must
		// still select H1 without a second transport or unchecked dial.
		if req.ProtoMajor != 1 || req.TLS == nil || (req.TLS.NegotiatedProtocol != "" && req.TLS.NegotiatedProtocol != "http/1.1") || req.URL.Path != "/allowed/socket" || req.Header.Get("Authorization") != "Bearer "+secretRouteReal || req.Header.Get("Sec-WebSocket-Extensions") != "" {
			firstRead <- errors.New("classic upgrade routing, credential or extension changed")
			http.Error(w, "wrong checked upgrade", http.StatusBadRequest)
			return
		}
		conn, buffer, err := http.NewResponseController(w).Hijack()
		if err != nil {
			firstRead <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\nX-Upgrade-Echo: %s\r\n\r\n", accept, secretRouteReal); err != nil {
			firstRead <- err
			return
		}
		if err := buffer.Flush(); err != nil {
			firstRead <- err
			return
		}
		message, err := wsReadTestMessage(buffer)
		if err == nil && string(message) != "guest kept" {
			err = errors.New("innocent client message changed")
		}
		firstRead <- err
		if err != nil {
			return
		}
		// The real credential crosses FIN=false/continuation boundaries.
		plain := []byte("echo|" + secretRouteReal + "|kept")
		wire := append(wsTestWire(1, false, false, plain[:11]), wsTestWire(0, true, false, plain[11:])...)
		if _, err := conn.Write(wire); err != nil {
			lastRead <- err
			return
		}
		// Inspect partial frames as well as the complete logical message.
		// A filter may forward an innocent prefix before failing closed.
		var received []byte
		for range 64 {
			frame, err := wsReadTestFrame(buffer)
			if bytes.Contains(frame.payload, []byte(secretRouteReal)) {
				lastRead <- errors.New("client path injected a real value")
				return
			}
			received = append(received, frame.payload...)
			if bytes.Contains(received, []byte(sentinel)) {
				lastRead <- errors.New("client stand-in escaped the actual message tripwire")
				return
			}
			if err != nil || frame.opcode == 8 {
				if bytes.Contains(received, []byte("probe|")) && bytes.Contains(received, []byte("|kept")) {
					lastRead <- errors.New("misused client message completed at origin")
					return
				}
				lastRead <- nil
				return
			}
			if frame.final {
				lastRead <- errors.New("client stand-in was forwarded as a complete data message")
				return
			}
		}
		lastRead <- errors.New("client refusal did not finish its origin stream")
	}))
	sentinel = f.sentinel()
	conn, err := f.dialTLS(t.Context(), 1, "api.example.com", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(conn, "GET /allowed/socket HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n", secretRouteAuthority, f.sentinel(), key); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Sec-WebSocket-Accept") != accept || response.Header.Get("X-Upgrade-Echo") != f.sentinel() || response.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatal("bound upgrade failed its exact public handshake or leaked a response credential")
	}
	if _, err := conn.Write(wsTestWire(1, true, true, []byte("guest kept"))); err != nil {
		t.Fatal(err)
	}
	wsRequireResult(t, firstRead)
	message, err := wsReadTestMessage(reader)
	if err != nil || string(message) != "echo|"+f.sentinel()+"|kept" || bytes.Contains(message, []byte(secretRouteReal)) {
		t.Fatal("fragmented server credential was not rewritten through the actual 101 route")
	}
	if _, err := conn.Write(wsTestWire(1, true, true, []byte("probe|"+f.sentinel()+"|kept"))); err != nil {
		// Immediate owned shutdown may win the final guest write. The
		// origin observation below is the security assertion.
		_ = conn.Close()
	}
	wsRequireResult(t, lastRead)
	_ = conn.Close()
	entry := f.audit(t, http.MethodGet, "/allowed/socket")
	if entry.Rule != "secret_misuse" || entry.Decision != "deny" {
		t.Fatal("client WebSocket misuse lost its names-only audit")
	}
	if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("classic upgrade escaped its one checked origin owner")
	}
}

func TestHTTPSecretRouteMintedStandInIsPublishedBeforeGuestBytesAndReusedUnderTheSameName(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			minted := "sk-ant-api03-" + strings.Repeat("A", 93) + "AA"
			observed := make(chan error, 2)
			f := newSecretRouteFixtureWithAck(t, protocol, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				want := secretRouteReal
				if req.URL.Path == "/allowed/reuse" {
					want = minted
				}
				if req.Header.Get("Authorization") != "Bearer "+want {
					observed <- errors.New("same-name request variant injected the wrong current value")
					http.Error(w, "wrong credential identity", 400)
					return
				}
				observed <- nil
				if req.URL.Path == "/allowed/mint" {
					_, _ = io.WriteString(w, "mint=\"")
					_ = http.NewResponseController(w).Flush()
					for _, b := range []byte(minted) {
						if _, err := w.Write([]byte{b}); err != nil {
							return
						}
					}
					_, _ = io.WriteString(w, "\"")
					return
				}
				_, _ = io.WriteString(w, "checked")
			}), false)
			type answer struct {
				response *http.Response
				body     []byte
				err      error
			}
			finished := make(chan struct{})
			answers := make(chan answer, 1)
			req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, "https://"+secretRouteAuthority+"/allowed/mint", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.sentinel())
			go func() {
				defer close(finished)
				response, err := f.client.Do(req)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				answers <- answer{response, body, err}
			}()
			t.Cleanup(func() {
				f.cancel()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("owned mint route request did not join")
				}
			})
			event := f.control.nextMint(t)
			select {
			case <-answers:
				t.Fatal("minted response completed before its exact public receipt was acknowledged")
			case <-time.After(100 * time.Millisecond):
			}
			if err := f.control.acknowledge(event, true); err != nil {
				t.Fatal(err)
			}
			var got answer
			select {
			case got = <-answers:
			case <-time.After(5 * time.Second):
				t.Fatal("acknowledged mint response did not finish")
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			response, body := got.response, got.body
			if event.Version != MemberRegistryV2 || event.Generation != f.initial.Generation || event.Owner != secretRouteName || event.State.Authority != secretRouteAuthority || event.State.Identity == "" || event.Receipt == "" {
				t.Fatal("real route omitted its exact public mint identity")
			}
			standIn := string(event.State.Sentinel)
			if response.StatusCode != http.StatusOK || string(body) != "mint=\""+standIn+"\"" || bytes.Contains(body, []byte(minted)) || len(standIn) != len(minted) {
				t.Fatal("server-issued credential escaped before canonical publication")
			}
			if f.control.eventCount.Load() != 1 {
				t.Fatal("one server value did not reserve exactly one public state event")
			}
			response = secretRouteRequest(t, f, http.MethodGet, "/allowed/reuse", "Bearer "+standIn)
			if body := secretRouteRead(t, response); response.StatusCode != http.StatusOK || string(body) != "checked" {
				t.Fatal("actual minted variant could not be reused under its configured name")
			}
			for range 2 {
				if err := <-observed; err != nil {
					t.Fatal(err)
				}
			}
			if f.lookups.Load() != 2 || f.dials.Load() != 2 || f.requests.Load() != 2 {
				t.Fatal("mint/reuse changed checked per-request origin ownership")
			}
			// The explicit ACK here is an owned test peer. Durable supervisor file
			// storage needs its independent end-to-end persistence test.
		})
	}
}

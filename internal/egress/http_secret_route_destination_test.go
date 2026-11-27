package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPSecretRouteRawHostAndCaseAliasCredentialsCannotReachTheOrigin(t *testing.T) {
	for _, kind := range []string{"host", "absolute-host", "case-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := newSecretRouteFixture(t, 1, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.NotFoundHandler())
			conn, err := f.dialTLS(t.Context(), 1, "api.example.com", "api.example.com")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			target, host := "/allowed/probe", secretRouteAuthority
			credential := "Authorization: Bearer " + f.sentinel() + "\r\n"
			switch kind {
			case "host":
				host = f.sentinel() + ".example.com:443"
			case "absolute-host":
				target = "https://" + secretRouteAuthority + "/allowed/probe"
				host = f.sentinel() + ".example.com:443"
			case "case-alias":
				credential += "authorization: Bearer " + f.sentinel() + "\r\n"
			}
			if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n%sConnection: close\r\n\r\n", target, host, credential); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
			if err != nil {
				t.Fatal(err)
			}
			_ = secretRouteRead(t, response)
			if kind == "case-alias" {
				if response.StatusCode != http.StatusForbidden {
					t.Fatal("case-alias credential duplication was accepted")
				}
				if entry := f.audit(t, http.MethodGet, "/allowed/probe"); entry.Rule != "secret_misuse" {
					t.Fatal("case-alias refusal skipped the named credential pin")
				}
			} else if response.StatusCode != http.StatusBadRequest {
				t.Fatal("raw Host mismatch was not rejected by ingress canonicalisation")
			}
			// Structural Host mismatches are deliberately refused before the
			// credential helper. A generic protocol audit is correct here.
			f.requireNoOrigin(t)
		})
	}
}

func TestHTTPSecretRouteWrongAuthorityAndCleartextNeverSubstituteCredentials(t *testing.T) {
	for _, kind := range []string{"wrong-host", "http80"} {
		t.Run(kind, func(t *testing.T) {
			rules := []api.Rule{{Allow: true, Target: secretRouteAuthority + "/allowed"}}
			host, port := "other.example.com", 443
			if kind == "http80" {
				host, port = "api.example.com", 80
			}
			rules = append(rules, api.Rule{Allow: true, Target: fmt.Sprintf("%s:%d/allowed", host, port)})
			f := newSecretRouteFixture(t, 1, 2, secretRouteArgs(t, secretRoutePolicy(t, rules...)), http.NotFoundHandler())
			var conn net.Conn
			var err error
			target := "/allowed/probe"
			if kind == "http80" {
				conn, err = f.pipe(func(server net.Conn) {
					f.control.server.ServeFlow(f.ctx, server, StreamHeader{Via: "explicit", Host: host, Port: port})
				})
				target = "http://api.example.com:80/allowed/probe"
			} else {
				conn, err = f.dialTLS(t.Context(), 1, host, host)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s:%d\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", target, host, port, f.sentinel()); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
			if err != nil {
				t.Fatal(err)
			}
			_ = secretRouteRead(t, response)
			if response.StatusCode != http.StatusForbidden {
				t.Fatal("wrong authority or cleartext accepted a bound stand-in")
			}
			if entry := f.audit(t, http.MethodGet, "/allowed/probe"); entry.Rule != "secret_misuse" {
				t.Fatal("credential misuse was hidden by a generic network refusal")
			}
			f.requireNoOrigin(t)
		})
	}
}

func TestHTTPSecretRoutePublicStandInsTripDNSAndSNIBeforeResolution(t *testing.T) {
	for _, kind := range []string{"dns", "sni", "stream-host"} {
		t.Run(kind, func(t *testing.T) {
			// Open policy means a missing tripwire cannot accidentally pass
			// this test because the canonical engine denied an unknown host.
			pol := onceTestPolicy(t, api.PresetOpen)
			f := newSecretRouteFixture(t, 1, 2, secretRouteArgs(t, pol), http.NotFoundHandler())
			misuseHost := strings.ToUpper(f.sentinel()) + ".example.com"
			switch kind {
			case "dns":
				query := buildSingleDNSQuery(t, 0x2718, true, misuseHost+".", dnsmessage.TypeA, dnsmessage.ClassINET)
				reply := unpackDNSReply(t, f.control.server.AnswerDNS(t.Context(), query))
				if reply.Header.ID != 0x2718 || reply.Header.RCode != dnsmessage.RCodeRefused || len(reply.Answers) != 0 {
					t.Fatal("actual DNS entrypoint did not refuse the public stand-in")
				}
				if entry := f.audit(t, "DNS", ""); entry.Rule != "secret_misuse" || entry.Mode != "dns" {
					t.Fatal("DNS was refused without the public credential tripwire")
				}
			case "sni":
				if conn, err := f.dialTLS(t.Context(), 1, misuseHost, "api.example.com"); err == nil {
					_ = conn.Close()
					t.Fatal("misused SNI completed an inspected TLS handshake")
				}
				if entry := f.audit(t, "TLS", ""); entry.Rule != "secret_misuse" {
					t.Fatal("a generic SNI mismatch hid a missing stand-in tripwire")
				}
			case "stream-host":
				if conn, err := f.dialTLS(t.Context(), 1, misuseHost, misuseHost); err == nil {
					_ = conn.Close()
					t.Fatal("misused physical stream host completed a handshake")
				}
				if entry := f.audit(t, "", ""); entry.Rule != "secret_misuse" {
					t.Fatal("raw stream destination skipped the stand-in tripwire")
				}
			}
			f.requireNoOrigin(t)
		})
	}
}

func TestHTTPSecretRouteDeclaredCredentialTrailersFailBeforeOriginWrites(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			f := newSecretRouteFixture(t, protocol, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.NotFoundHandler())
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+secretRouteAuthority+"/allowed/trailer", io.NopCloser(strings.NewReader("guest body")))
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = -1
			req.Header.Set("Authorization", "Bearer "+f.sentinel())
			// X-Api-Key is generally a legal trailer name. The bound-secret
			// pin must reject its declaration before any request body read.
			req.Trailer = http.Header{"X-Api-Key": []string{"attacker-key"}}
			response, err := f.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = secretRouteRead(t, response)
			if response.StatusCode != http.StatusForbidden {
				t.Fatal("credential trailer declaration reached origin admission")
			}
			if entry := f.audit(t, http.MethodPost, "/allowed/trailer"); entry.Rule != "secret_misuse" {
				t.Fatal("declared credential trailer skipped the bound pin")
			}
			f.requireNoOrigin(t)
		})
	}
}

package egress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPProtocolEncodedBodiesAreCanonicalAtBothHops(t *testing.T) {
	for _, guestProtocol := range []int{1, 2} {
		for _, originProtocol := range []int{1, 2} {
			for _, encoding := range []string{"gzip", "zlib", "raw"} {
				t.Run(fmt.Sprintf("guest%d_origin%d_%s", guestProtocol, originProtocol, encoding), func(t *testing.T) {
					wireEncoding := "deflate"
					if encoding == "gzip" {
						wireEncoding = "gzip"
					}
					const requestBody = "request canonical bytes"
					const responseBody = "response canonical bytes"
					encodedRequest := encodedBodyFixture(t, encoding, strings.NewReader(requestBody))
					encodedResponse := encodedBodyFixture(t, encoding, strings.NewReader(responseBody))
					observed := make(chan error, 1)
					h := newHTTPProtocolOracle(t, originProtocol == 2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if err := protocolOracleRequestError(r, originProtocol, http.MethodPost, "/allowed/encoded"); err != nil {
							observed <- err
							http.Error(w, "bad oracle request", 500)
							return
						}
						body, err := io.ReadAll(r.Body)
						if err != nil || string(body) != requestBody || r.Header.Get("Content-Encoding") != "" || r.Header.Get("Accept-Encoding") != "identity" {
							observed <- fmt.Errorf("origin did not see canonical bytes: body=%q encoding=%q accept=%q error=%v", body, r.Header.Get("Content-Encoding"), r.Header.Get("Accept-Encoding"), err)
							http.Error(w, "bad decoded body", 500)
							return
						}
						observed <- nil
						w.Header().Set("Content-Encoding", wireEncoding)
						w.Header().Set("Content-Length", strconv.Itoa(len(encodedResponse)))
						_, _ = w.Write(encodedResponse)
					}))
					request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example.com/allowed/%65ncoded?kept=yes", bytes.NewReader(encodedRequest))
					if err != nil {
						t.Fatal(err)
					}
					request.Header.Set("Content-Encoding", wireEncoding)
					request.Header.Set("Accept-Encoding", "gzip, deflate")
					response, err := h.client(t, guestProtocol).Do(request)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					body, err := io.ReadAll(response.Body)
					if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != guestProtocol || string(body) != responseBody || response.Header.Get("Content-Encoding") != "" || response.Uncompressed {
						t.Fatalf("guest did not receive proxy-decoded bytes: protocol=%s status=%d encoding=%q uncompressed=%v body=%q error=%v", response.Proto, response.StatusCode, response.Header.Get("Content-Encoding"), response.Uncompressed, body, err)
					}
					if err := <-observed; err != nil {
						t.Fatal(err)
					}
					if h.dials.Load() != 1 {
						t.Fatalf("encoded request used %d checked origin dials", h.dials.Load())
					}
					requireProtocolOracleAudit(t, h, http.MethodPost, "/allowed/encoded", http.StatusOK)
				})
			}
		}
	}
}

func TestHTTPProtocolBadRequestEncodingFailsBeforeTheOriginDial(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, encoding := range []string{"gzip", "br", "gzip, gzip"} {
			t.Run(fmt.Sprintf("guest%d_%s", protocol, encoding), func(t *testing.T) {
				h := newHTTPProtocolOracle(t, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unexpected origin request", 500) }))
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example.com/allowed/encoded?kept=yes", strings.NewReader("broken encoding"))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Encoding", encoding)
				response, err := h.client(t, protocol).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				_, _ = io.Copy(io.Discard, response.Body)
				if response.StatusCode != http.StatusUnsupportedMediaType || h.dials.Load() != 0 || h.lookups.Load() != 0 {
					t.Fatalf("bad encoding returned %d after dials=%d lookups=%d", response.StatusCode, h.dials.Load(), h.lookups.Load())
				}
			})
		}
	}
}

func TestHTTPProtocolBadResponseEncodingCannotCompleteSuccessfully(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, mode := range []string{"bad-header", "bad-checksum"} {
			t.Run(fmt.Sprintf("guest%d_%s", protocol, mode), func(t *testing.T) {
				encoded := []byte("not a gzip header")
				if mode == "bad-checksum" {
					encoded = encodedBodyFixture(t, "gzip", strings.NewReader("decoded response"))
					encoded[len(encoded)-1] ^= 0xff
				}
				originOffered := make(chan struct{})
				h := newHTTPProtocolOracle(t, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Encoding", "gzip")
					w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
					_, _ = w.Write(encoded)
					close(originOffered)
				}))
				response, err := h.client(t, protocol).Get("https://api.example.com/allowed/encoded?kept=yes")
				select {
				case <-originOffered:
				case <-time.After(5 * time.Second):
					t.Fatal("invalid encoded response was never offered by the owned origin")
				}
				if h.dials.Load() != 1 {
					t.Fatalf("invalid origin encoding used %d checked dials", h.dials.Load())
				}
				if mode == "bad-checksum" && err != nil {
					return
				} // Aborting before guest headers is also a failed stream.
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, readErr := io.ReadAll(response.Body)
				if mode == "bad-header" {
					if response.StatusCode != http.StatusBadGateway {
						t.Fatalf("bad gzip header returned %d body=%q", response.StatusCode, body)
					}
				} else if response.StatusCode != http.StatusBadGateway && (response.StatusCode != http.StatusOK || readErr == nil) {
					t.Fatalf("corrupt streaming body completed cleanly: status=%d body=%q error=%v", response.StatusCode, body, readErr)
				}
			})
		}
	}
}

func TestHTTPProtocolCorruptRequestChecksumNeverBecomesACleanOriginBody(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprintf("guest%d", protocol), func(t *testing.T) {
			encoded := encodedBodyFixture(t, "gzip", strings.NewReader("request with a corrupt checksum"))
			encoded[len(encoded)-1] ^= 0xff
			originResult := make(chan error, 1)
			h := newHTTPProtocolOracle(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.Copy(io.Discard, r.Body)
				originResult <- err
				http.Error(w, "incomplete request", http.StatusBadGateway)
			}))
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.example.com/allowed/encoded?kept=yes", bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Encoding", "gzip")
			response, err := h.client(t, protocol).Do(request)
			if err == nil {
				defer response.Body.Close()
				_, _ = io.Copy(io.Discard, response.Body)
				if response.StatusCode != http.StatusBadGateway {
					t.Fatalf("corrupt upload returned %d", response.StatusCode)
				}
			}
			select {
			case originErr := <-originResult:
				if originErr == nil {
					t.Fatal("invalid checksum became a complete origin request")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("checked origin body did not end after compressed input failure")
			}
		})
	}
}

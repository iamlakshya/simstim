package egress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPProtocolSafeRequestAndResponseTrailersRemainVisible(t *testing.T) {
	for _, guestProtocol := range []int{1, 2} {
		for _, originProtocol := range []int{1, 2} {
			t.Run(fmt.Sprintf("guest%d_origin%d", guestProtocol, originProtocol), func(t *testing.T) {
				observed := make(chan error, 1)
				h := newHTTPProtocolOracle(t, originProtocol == 2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := protocolOracleRequestError(r, originProtocol, http.MethodPost, "/allowed/upload"); err != nil {
						observed <- err
						http.Error(w, "bad oracle request", 500)
						return
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "request bytes" || r.Trailer.Get("X-Request-Checksum") != "request-digest" {
						observed <- fmt.Errorf("origin body=%q trailer=%v error=%v", body, r.Trailer, err)
						http.Error(w, "lost request trailer", 500)
						return
					}
					observed <- nil
					w.Header().Set("Trailer", "X-Origin-Checksum, X-Origin-Count")
					_, _ = io.WriteString(w, "response bytes")
					w.Header().Set("X-Origin-Checksum", "response-digest")
					w.Header().Set("X-Origin-Count", "14")
				}))
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.example.com/allowed/%75pload?kept=yes", strings.NewReader("request bytes"))
				if err != nil {
					t.Fatal(err)
				}
				request.ContentLength = -1
				request.Trailer = http.Header{"X-Request-Checksum": []string{"request-digest"}}
				request.Header.Set("Proxy-Authorization", "Basic fixture-only")
				response, err := h.client(t, guestProtocol).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != guestProtocol || string(body) != "response bytes" || response.Trailer.Get("X-Origin-Checksum") != "response-digest" || response.Trailer.Get("X-Origin-Count") != "14" {
					t.Fatalf("guest response=%s %d body=%q trailers=%v error=%v", response.Proto, response.StatusCode, body, response.Trailer, err)
				}
				if err := <-observed; err != nil {
					t.Fatal(err)
				}
				if h.dials.Load() != 1 {
					t.Fatalf("one request opened %d origin dials", h.dials.Load())
				}
				requireProtocolOracleAudit(t, h, http.MethodPost, "/allowed/upload", http.StatusOK)
			})
		}
	}
}

func TestHTTPProtocolForbiddenOriginTrailersCannotReachTheGuest(t *testing.T) {
	for _, field := range []string{"Authorization", "Proxy-Authorization", "Host", "Connection"} {
		t.Run(field, func(t *testing.T) {
			offered := make(chan error, 1)
			h := newHTTPProtocolOracle(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := protocolOracleRequestError(r, 1, http.MethodGet, "/allowed/trailers"); err != nil {
					offered <- err
					http.Error(w, "bad oracle request", 500)
					return
				}
				conn, buffer, err := http.NewResponseController(w).Hijack()
				if err != nil {
					offered <- err
					return
				}
				defer conn.Close()
				_, _ = fmt.Fprintf(buffer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Origin-Checksum, %s\r\nConnection: close\r\n\r\n4\r\nsafe\r\n0\r\nX-Origin-Checksum: digest\r\n%s: forbidden-value\r\n\r\n", field, field)
				offered <- buffer.Flush()
			}))
			response, err := h.client(t, 1).Get("https://api.example.com/allowed/trailers?kept=yes")
			select {
			case originErr := <-offered:
				if originErr != nil {
					t.Fatal(originErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the hostile response fixture was never written")
			}
			if h.dials.Load() != 1 {
				t.Fatalf("hostile response fixture had %d checked dials", h.dials.Load())
			}
			if err != nil {
				t.Fatalf("declared forbidden trailer did not return the required pre-header 502: %v", err)
			}
			defer response.Body.Close()
			_, _ = io.ReadAll(response.Body)
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("declared forbidden trailer returned %d, want 502 before guest headers", response.StatusCode)
			}
			if response.Header.Get(field) == "forbidden-value" || response.Trailer.Get(field) != "" {
				t.Fatalf("forbidden origin trailer crossed the proxy: header=%v trailer=%v", response.Header, response.Trailer)
			}
		})
	}
}

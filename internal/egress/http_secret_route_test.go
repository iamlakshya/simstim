package egress

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPSecretRouteReplacesOnlyTheNamedHTTPSHeaderAndScrubsEveryResponseSurface(t *testing.T) {
	formats := []struct{ name, header, format, encoding string }{
		{"raw", "X-Api-Key", "{value}", "identity"},
		{"bearer", "Authorization", "Bearer {value}", "gzip"},
		{"basic", "Authorization", "basic:fixed:{value}", "deflate"},
	}
	for _, guest := range []int{1, 2} {
		for _, origin := range []int{1, 2} {
			for _, format := range formats {
				t.Run(fmt.Sprintf("guest%d_origin%d_%s", guest, origin, format.name), func(t *testing.T) {
					args := secretRouteArgs(t, secretRoutePolicy(t))
					args.Binds[secretRouteName] = api.SecretBind{Hosts: []string{"api.example.com"}, Header: format.header, Format: format.format}
					payload := secretRouteEncoded(t, format.encoding, []byte("before|"+secretRouteReal+"|after"))
					checked := make(chan error, 1)
					f := newSecretRouteFixture(t, guest, origin, args, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						failure := secretRouteOriginRequest(req, format.name, format.header)
						checked <- failure
						if failure != nil {
							http.Error(w, "wrong request form", http.StatusBadRequest)
							return
						}
						w.Header().Set("X-Origin-Echo", "header|"+secretRouteReal+"|kept")
						w.Header().Set("Trailer", "X-Origin-Tail")
						if format.encoding != "identity" {
							w.Header().Set("Content-Encoding", format.encoding)
						}
						w.WriteHeader(http.StatusOK)
						for offset := 0; offset < len(payload); offset += 7 {
							end := min(offset+7, len(payload))
							if _, err := w.Write(payload[offset:end]); err != nil {
								return
							}
							if err := http.NewResponseController(w).Flush(); err != nil {
								return
							}
						}
						w.Header().Set("X-Origin-Tail", "trailer|"+secretRouteReal+"|kept")
					}))
					standIn := f.sentinel()
					if len(standIn) != len(secretRouteReal) || standIn == secretRouteReal {
						t.Fatal("invalid actual same-length public stand-in")
					}
					req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+secretRouteAuthority+"/allowed/echo?kept=yes", strings.NewReader("guest body kept"))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set(format.header, secretRouteCredential(format.name, standIn))
					req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
					req.Header.Set("X-Unrelated", "kept")
					response, err := f.client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body := secretRouteRead(t, response)
					if response.StatusCode != http.StatusOK || response.ProtoMajor != guest {
						t.Fatalf("guest protocol/status = %s %d", response.Proto, response.StatusCode)
					}
					if string(body) != "before|"+standIn+"|after" || bytes.Contains(body, []byte(secretRouteReal)) {
						t.Fatal("real credential escaped through the decoded response body")
					}
					if response.Header.Get("X-Origin-Echo") != "header|"+standIn+"|kept" || response.Trailer.Get("X-Origin-Tail") != "trailer|"+standIn+"|kept" {
						t.Fatal("header or final trailer escaped response substitution")
					}
					if response.Header.Get("Content-Encoding") != "" {
						t.Fatal("decoded response retained encoded framing")
					}
					if err := <-checked; err != nil {
						t.Fatal(err)
					}
					if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
						t.Fatal("one request did not retain one checked origin owner")
					}
					entry := f.audit(t, http.MethodPost, "/allowed/echo")
					if entry.Status != http.StatusOK || entry.Decision != "allow" || entry.Mode != "intercept" || entry.Up <= 0 || entry.Down <= 0 {
						t.Fatal("request audit lost verdict, mode or measured origin bytes")
					}
				})
			}
		}
	}
}

func secretRouteCredential(format, value string) string {
	switch format {
	case "raw":
		return value
	case "bearer":
		return "bEaReR " + value
	case "basic":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte("fixed:"+value))
	default:
		panic("unknown route fixture format")
	}
}

func secretRouteOriginRequest(req *http.Request, format, header string) error {
	if req.Method != http.MethodPost || req.URL.Path != "/allowed/echo" || req.URL.RawQuery != "kept=yes" || req.Host != secretRouteAuthority {
		return errors.New("named replacement changed request routing")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil || string(body) != "guest body kept" {
		return errors.New("named replacement changed request body")
	}
	if req.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" || req.Header.Get("X-Unrelated") != "kept" || req.Header.Get("Accept-Encoding") != "identity" {
		return errors.New("unrelated headers or forced encoding changed")
	}
	if len(req.Header.Values(header)) != 1 {
		return errors.New("origin received duplicate credentials")
	}
	if format == "basic" {
		user, password, ok := req.BasicAuth()
		if !ok || user != "fixed" || password != secretRouteReal {
			return errors.New("Basic replacement did not retain its fixed username")
		}
	} else {
		value := req.Header.Get(header)
		if format == "bearer" {
			if len(value) < 7 || !strings.EqualFold(value[:7], "Bearer ") {
				return errors.New("Bearer scheme changed")
			}
			value = value[7:]
		}
		if value != secretRouteReal {
			return errors.New("named stand-in did not become its actual current value")
		}
	}
	return nil
}

func secretRouteEncoded(t *testing.T, encoding string, input []byte) []byte {
	t.Helper()
	if encoding == "identity" {
		return input
	}
	var output bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&output)
	case "deflate":
		writer = zlib.NewWriter(&output)
	default:
		t.Fatal("unknown fixture response encoding")
	}
	if _, err := writer.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

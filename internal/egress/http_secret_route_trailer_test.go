package egress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPSecretRouteLateRequestTrailerStopsBeforeTheFinalOriginFields(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			started := make(chan struct{}, 1)
			observed := make(chan error, 1)
			f := newSecretRouteFixture(t, protocol, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var first [1]byte
				if _, err := io.ReadFull(req.Body, first[:]); err != nil {
					observed <- errors.New("origin never received the intentionally early innocent body prefix")
					return
				}
				started <- struct{}{}
				_, err := io.Copy(io.Discard, req.Body)
				if err == nil {
					observed <- errors.New("late misused trailer made the origin request complete")
					return
				}
				if req.Trailer.Get("X-Guest-Data") != "" {
					observed <- errors.New("refused final trailer reached the origin")
					return
				}
				observed <- nil
			}))
			input, upload := io.Pipe()
			release := make(chan struct{})
			var releaseOnce sync.Once
			finishUpload := func() { releaseOnce.Do(func() { close(release) }) }
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				defer upload.Close()
				if _, err := io.WriteString(upload, strings.Repeat("innocent body bytes\n", 4096)); err != nil {
					return
				}
				select {
				case <-release:
				case <-f.ctx.Done():
				}
			}()
			req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, "https://"+secretRouteAuthority+"/allowed/late", input)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = -1
			req.Header.Set("Authorization", "Bearer "+f.sentinel())
			req.Trailer = http.Header{"X-Guest-Data": []string{f.sentinel()}}
			type answer struct {
				response *http.Response
				body     []byte
				err      error
			}
			answers := make(chan answer, 1)
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				response, err := f.client.Do(req)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				answers <- answer{response, body, err}
			}()
			t.Cleanup(func() {
				finishUpload()
				_ = upload.Close()
				_ = input.Close()
				f.cancel()
				for _, done := range []<-chan struct{}{writerDone, requestDone} {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("owned late-trailer operation did not join")
					}
				}
			})
			waitHTTPProgress(t, started, "the actual early origin body prefix")
			finishUpload()
			select {
			case err := <-observed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("late trailer did not end its actual origin body")
			}
			select {
			case got := <-answers:
				if got.err == nil && got.response.StatusCode == http.StatusOK {
					t.Fatal("late credential trailer produced a successful completed request")
				}
				if bytes.Contains(got.body, []byte(secretRouteReal)) {
					t.Fatal("late request refusal returned a real credential")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("late trailer left the guest request alive")
			}
			if entry := f.audit(t, http.MethodPost, "/allowed/late"); entry.Rule != "secret_misuse" || entry.Decision != "deny" {
				t.Fatal("late request trailer lost its actual credential refusal audit")
			}
			if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
				t.Fatal("late trailer did not retain one actual origin request")
			}
		})
	}
}

func TestHTTPSecretRouteLateForbiddenCompressedResponseCannotEmitTrailerValues(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, encoding := range []string{"identity", "gzip", "deflate"} {
			t.Run(fmt.Sprintf("http%d_%s", protocol, encoding), func(t *testing.T) {
				offered := make(chan error, 1)
				payload := secretRouteEncoded(t, encoding, []byte("body|"+secretRouteReal+"|kept"))
				f := newSecretRouteFixture(t, protocol, 1, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Header.Get("Authorization") != "Bearer "+secretRouteReal {
						offered <- errors.New("hostile response fixture missed actual named substitution")
						http.Error(w, "wrong credential", 400)
						return
					}
					conn, buffer, err := http.NewResponseController(w).Hijack()
					if err != nil {
						offered <- err
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
					_, _ = io.WriteString(buffer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Origin-Tail\r\n")
					if encoding != "identity" {
						_, _ = fmt.Fprintf(buffer, "Content-Encoding: %s\r\n", encoding)
					}
					_, _ = io.WriteString(buffer, "\r\n")
					for start := 0; start < len(payload); start += 5 {
						end := min(start+5, len(payload))
						_, _ = fmt.Fprintf(buffer, "%x\r\n", end-start)
						_, _ = buffer.Write(payload[start:end])
						_, _ = io.WriteString(buffer, "\r\n")
					}
					// Only X-Origin-Tail was declared. The late authentication
					// field must never cross a decoded EOF as guest metadata.
					_, _ = fmt.Fprintf(buffer, "0\r\nX-Origin-Tail: %s\r\nAuthorization: %s\r\n\r\n", secretRouteReal, secretRouteReal)
					offered <- buffer.Flush()
				}))
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+secretRouteAuthority+"/allowed/late-response", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+f.sentinel())
				response, requestErr := f.client.Do(req)
				select {
				case err := <-offered:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("hostile trailer fixture was never written")
				}
				if requestErr == nil {
					body, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK && readErr == nil {
						t.Fatal("forbidden final response metadata appeared complete")
					}
					if bytes.Contains(body, []byte(secretRouteReal)) {
						t.Fatal("compressed late-trailer failure exposed a body credential")
					}
					for _, fields := range []http.Header{response.Header, response.Trailer} {
						if fields.Get("Authorization") != "" {
							t.Fatal("forbidden final response field reached the guest")
						}
						for _, values := range fields {
							for _, value := range values {
								if strings.Contains(value, secretRouteReal) {
									t.Fatal("late response metadata exposed a real credential")
								}
							}
						}
					}
				}
				if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
					t.Fatal("hostile response escaped the actual checked origin route")
				}
			})
		}
	}
}

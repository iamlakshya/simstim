package egress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPSecretRouteLeavesOrdinaryRequestBodyDataAndSafeTrailersUnchanged(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, encoding := range []string{"identity", "gzip", "deflate"} {
			t.Run(fmt.Sprintf("http%d_%s", protocol, encoding), func(t *testing.T) {
				var standIn string
				observed := make(chan error, 1)
				f := newSecretRouteFixture(t, protocol, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					body, err := io.ReadAll(req.Body)
					if err != nil || string(body) != "body|"+standIn+"|kept" || strings.Contains(string(body), secretRouteReal) {
						observed <- errors.New("ordinary request body was changed or injected with a real value")
						http.Error(w, "body changed", http.StatusBadRequest)
						return
					}
					if req.Header.Get("Authorization") != "Bearer "+secretRouteReal || req.Header.Get("Content-Encoding") != "" || req.Trailer.Get("X-Guest-Checksum") != "kept-digest" {
						observed <- errors.New("named header substitution, decoding or safe request trailer was lost")
						http.Error(w, "request metadata changed", http.StatusBadRequest)
						return
					}
					observed <- nil
					_, _ = w.Write(body)
				}))
				standIn = f.sentinel()
				payload := secretRouteEncoded(t, encoding, []byte("body|"+standIn+"|kept"))
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+secretRouteAuthority+"/allowed/body", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.ContentLength = -1
				req.Header.Set("Authorization", "Bearer "+standIn)
				if encoding != "identity" {
					req.Header.Set("Content-Encoding", encoding)
				}
				req.Trailer = http.Header{"X-Guest-Checksum": []string{"kept-digest"}}
				response, err := f.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body := secretRouteRead(t, response)
				if response.StatusCode != http.StatusOK || response.ProtoMajor != protocol || string(body) != "body|"+standIn+"|kept" {
					var originErr error
					select {
					case originErr = <-observed:
					default:
						originErr = errors.New("origin validation has not completed")
					}
					auditRule, auditDecision := "not-observed", "not-observed"
					select {
					case audit := <-f.control.audits:
						auditRule, auditDecision = audit.Rule, audit.Decision
					default:
					}
					t.Fatalf("ordinary body data was rewritten or its safe trailer refused: status=%d protocol=%d body_match=%t origin_check=%v audit_rule=%s audit_decision=%s lookups=%d dials=%d requests=%d origin_writes=%d credential_refusal=%t cancelled_refusal=%t policy_refusal=%t", response.StatusCode, response.ProtoMajor, string(body) == "body|"+standIn+"|kept", originErr, auditRule, auditDecision, f.lookups.Load(), f.dials.Load(), f.requests.Load(), f.originWrites.Load(), strings.Contains(string(body), "request credential is not permitted"), strings.Contains(string(body), "HTTP request was cancelled"), strings.Contains(string(body), "is not allowed"))
				}
				if err := <-observed; err != nil {
					t.Fatal(err)
				}
				if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
					t.Fatal("ordinary body handling changed checked origin ownership")
				}
			})
		}
	}
}

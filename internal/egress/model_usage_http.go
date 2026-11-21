package egress

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

// This observes successful decoded body writes, never origin read-ahead or
// protocol framing. ResponseController can still reach the original writer.
type modelUsageHTTPWriter struct {
	http.ResponseWriter
	turn     *ModelUsageTurnV1
	active   bool
	physical bool
	failed   atomic.Bool
}

func (w *modelUsageHTTPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *modelUsageHTTPWriter) Write(payload []byte) (int, error) {
	n, err := w.ResponseWriter.Write(payload)
	if w.active && !w.physical && n > 0 {
		w.turn.ForwardedV1(payload[:n])
	}
	if err != nil || n != len(payload) {
		w.failed.Store(true)
	}
	return n, err //nolint:wrapcheck // preserve the owned writer or reader result, including EOF
}

type modelUsageEOFBody struct {
	io.ReadCloser
	complete *atomic.Bool
}

func (body *modelUsageEOFBody) Read(payload []byte) (int, error) {
	n, err := body.ReadCloser.Read(payload)
	if errors.Is(err, io.EOF) {
		body.complete.Store(true)
	}
	return n, err //nolint:wrapcheck // preserve the owned writer or reader result, including EOF
}

func configureModelUsageResponse(response *http.Response, turn *ModelUsageTurnV1, complete *atomic.Bool) {
	mode := ModelUsageJSONV1
	kind, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err == nil && strings.EqualFold(kind, "text/event-stream") {
		mode = ModelUsageSSEV1
	}
	turn.ResponseV1(mode)
	response.Body = &modelUsageEOFBody{ReadCloser: response.Body, complete: complete}
}

func modelBudget(policy api.Policy) int64 {
	if policy.Network.ModelBudget != nil {
		return *policy.Network.ModelBudget
	}
	return 0
}

func modelUsageWire(model *modelRequest) string {
	if model == nil || model.guard == nil {
		return ""
	}
	if model.guard.Wire == "openai-responses" && model.request.Path == "/v1/chat/completions" {
		return "openai-chat-completions"
	}
	return model.guard.Wire
}

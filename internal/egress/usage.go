package egress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// TokenUsage holds model token counts reported by a provider response or
// accumulated across a session (spec 17.8).
type TokenUsage = api.TokenUsage

// UsageSummary is the session-wide token count returned by the "usage" ctl op.
type UsageSummary = api.SessionUsage

// shouldCountUsage reports whether a 2xx response on wire+path carries model
// generation usage (spec 17.8: count_tokens and GET /v1/models count nothing).
func shouldCountUsage(wire, method, cleanPath string, status int) bool {
	if status < 200 || status >= 300 || !strings.EqualFold(method, "POST") {
		return false
	}
	switch wire {
	case WireAnthropicMessages:
		return cleanPath == "/v1/messages"
	case WireOpenAIResponses, WireOpenAIChat:
		return cleanPath == "/v1/responses" || cleanPath == "/backend-api/codex/responses" || cleanPath == "/v1/chat/completions"
	default:
		return false
	}
}

// usageObserver incrementally parses SSE or plain JSON responses as bytes pass
// through to the client, without buffering full streams in memory.
type usageObserver struct {
	wire              string
	reqBytes          int64
	isSSE             bool
	respBytes         int64
	lineBuf           bytes.Buffer
	sseEvent          string
	sseData           []string
	sseDataBytes      int
	sseDataTooBig     bool
	plainBuf          bytes.Buffer
	plainTooBig       bool
	usage             TokenUsage
	hasUsage          bool
	sawAnthropicStart bool
}

const maxPlainUsageBytes = 8 << 20

func newUsageObserver(wire string, reqBytes int64, contentType string) *usageObserver {
	return &usageObserver{
		wire:     wire,
		reqBytes: reqBytes,
		isSSE:    strings.Contains(strings.ToLower(contentType), "text/event-stream"),
	}
}

func (o *usageObserver) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	o.respBytes += int64(len(p))
	if o.isSSE {
		o.feedSSE(p)
		return
	}
	if !o.plainTooBig {
		if o.plainBuf.Len()+len(p) > maxPlainUsageBytes {
			o.plainTooBig = true
			o.plainBuf.Reset()
		} else {
			_, _ = o.plainBuf.Write(p)
		}
	}
}

func (o *usageObserver) feedSSE(p []byte) {
	for _, b := range p {
		if b == '\n' {
			line := strings.TrimSuffix(o.lineBuf.String(), "\r")
			o.lineBuf.Reset()
			o.handleSSELine(line)
			continue
		}
		if o.lineBuf.Len() < 2<<20 {
			_ = o.lineBuf.WriteByte(b)
		}
	}
}

func (o *usageObserver) handleSSELine(line string) {
	if line == "" {
		o.flushSSEEvent()
		return
	}
	if strings.HasPrefix(line, ":") {
		return
	}
	if v, ok := strings.CutPrefix(line, "event:"); ok {
		o.sseEvent = strings.TrimSpace(v)
		return
	}
	if v, ok := strings.CutPrefix(line, "data:"); ok && !o.sseDataTooBig {
		part := strings.TrimSpace(v)
		if o.sseDataBytes+len(part)+1 > maxPlainUsageBytes {
			o.sseDataTooBig, o.sseData, o.sseDataBytes = true, o.sseData[:0], 0
			return
		}
		o.sseData = append(o.sseData, part)
		o.sseDataBytes += len(part) + 1
	}
}

func (o *usageObserver) flushSSEEvent() {
	tooBig := o.sseDataTooBig
	o.sseDataTooBig, o.sseDataBytes = false, 0
	if tooBig || len(o.sseData) == 0 {
		o.sseEvent, o.sseData = "", o.sseData[:0]
		return
	}
	data := strings.Join(o.sseData, "\n")
	ev := o.sseEvent
	o.sseEvent = ""
	o.sseData = o.sseData[:0]
	if data == "" || data == "[DONE]" {
		return
	}
	o.parseSSEPayload(ev, []byte(data))
}

func (o *usageObserver) parseSSEPayload(eventName string, data []byte) {
	switch o.wire {
	case WireAnthropicMessages:
		o.parseAnthropicSSE(eventName, data)
	case WireOpenAIResponses, WireOpenAIChat:
		o.parseOpenAISSE(eventName, data)
	}
}

func (o *usageObserver) parseAnthropicSSE(eventName string, data []byte) {
	var env struct {
		Type    string `json:"type"`
		Message struct {
			Model string          `json:"model"`
			Usage *anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage *anthropicUsage `json:"usage"`
	}
	if json.Unmarshal(data, &env) != nil {
		return
	}
	kind := env.Type
	if kind == "" {
		kind = eventName
	}
	switch kind {
	case "message_start":
		if env.Message.Model != "" {
			o.usage.Model = env.Message.Model
		}
		if env.Message.Usage != nil {
			o.sawAnthropicStart = true
			o.applyAnthropicUsage(env.Message.Usage, false)
		}
	case "message_delta":
		// Spec 17.8: message_delta usage counts are cumulative; replace present
		// fields rather than adding them.
		if env.Usage != nil {
			o.applyAnthropicUsage(env.Usage, true)
		}
	case "message_stop":
		if o.sawAnthropicStart {
			o.hasUsage = true
		}
	}
}

type anthropicUsage struct {
	InputTokens         *int64 `json:"input_tokens"`
	CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
	OutputTokens        *int64 `json:"output_tokens"`
	ServerToolUse       *struct {
		WebSearchRequests *int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
}

func (o *usageObserver) applyAnthropicUsage(u *anthropicUsage, final bool) {
	if final {
		o.hasUsage = true
	}
	if u.InputTokens != nil {
		o.usage.InputTokens = *u.InputTokens
	}
	if u.CacheCreationTokens != nil {
		o.usage.CacheCreationTokens = *u.CacheCreationTokens
	}
	if u.CacheReadTokens != nil {
		o.usage.CacheReadTokens = *u.CacheReadTokens
	}
	if u.OutputTokens != nil {
		o.usage.OutputTokens = *u.OutputTokens
	}
	if u.ServerToolUse != nil && u.ServerToolUse.WebSearchRequests != nil {
		o.usage.WebSearchRequests = *u.ServerToolUse.WebSearchRequests
	}
	o.usage.TotalTokens = o.usage.InputTokens + o.usage.CacheCreationTokens +
		o.usage.CacheReadTokens + o.usage.OutputTokens
}

type openAIUsage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	PromptTokens       *int64 `json:"prompt_tokens"`
	CompletionTokens   *int64 `json:"completion_tokens"`
	CacheWriteTokens   *int64 `json:"cache_write_tokens"`
	TotalTokens        *int64 `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (o *usageObserver) parseOpenAISSE(eventName string, data []byte) {
	var env struct {
		Type     string `json:"type"`
		Model    string `json:"model"`
		Response *struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
		} `json:"response"`
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(data, &env) != nil {
		return
	}
	if env.Model != "" {
		o.usage.Model = env.Model
	}
	if env.Response != nil && env.Response.Model != "" {
		o.usage.Model = env.Response.Model
	}
	kind := env.Type
	if kind == "" {
		kind = eventName
	}
	if env.Response != nil && env.Response.Usage != nil &&
		(kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed") {
		o.applyOpenAIUsage(env.Response.Usage)
		return
	}
	// Chat Completions streaming emits one chunk with non-null usage before [DONE].
	if env.Usage != nil {
		o.applyOpenAIUsage(env.Usage)
	}
}

func (o *usageObserver) applyOpenAIUsage(u *openAIUsage) {
	o.hasUsage = true
	if u.InputTokens != nil {
		o.usage.InputTokens = *u.InputTokens
	} else if u.PromptTokens != nil {
		o.usage.InputTokens = *u.PromptTokens
	}
	if u.OutputTokens != nil {
		o.usage.OutputTokens = *u.OutputTokens
	} else if u.CompletionTokens != nil {
		o.usage.OutputTokens = *u.CompletionTokens
	}
	if u.CacheWriteTokens != nil {
		o.usage.CacheCreationTokens = *u.CacheWriteTokens
	}
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens != nil {
		o.usage.CacheReadTokens = *u.InputTokensDetails.CachedTokens
	} else if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		o.usage.CacheReadTokens = *u.PromptTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ReasoningTokens != nil {
		o.usage.ReasoningTokens = *u.OutputTokensDetails.ReasoningTokens
	} else if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != nil {
		o.usage.ReasoningTokens = *u.CompletionTokensDetails.ReasoningTokens
	}
	// Spec 17.8: OpenAI's total_tokens already includes cached input tokens.
	if u.TotalTokens != nil {
		o.usage.TotalTokens = *u.TotalTokens
	} else {
		o.usage.TotalTokens = o.usage.InputTokens + o.usage.OutputTokens
	}
}

// Finish returns the final TokenUsage for the response, falling back to the
// spec 17.8 byte estimate (reqBytes + respBytes) / 4 rounded up when no usage
// was reported.
func (o *usageObserver) Finish() TokenUsage {
	if o.isSSE {
		if o.lineBuf.Len() > 0 {
			o.handleSSELine(strings.TrimSuffix(o.lineBuf.String(), "\r"))
			o.lineBuf.Reset()
		}
		o.flushSSEEvent()
	} else if !o.plainTooBig && o.plainBuf.Len() > 0 {
		o.parsePlainJSON(o.plainBuf.Bytes())
	}
	if o.hasUsage {
		return o.usage
	}
	est := o.usage
	est.Estimated = true
	est.TotalTokens = (o.reqBytes + o.respBytes + 3) / 4
	return est
}

func (o *usageObserver) parsePlainJSON(raw []byte) {
	if o.wire == WireAnthropicMessages {
		var body struct {
			Model string          `json:"model"`
			Usage *anthropicUsage `json:"usage"`
		}
		if json.Unmarshal(raw, &body) == nil {
			if body.Model != "" {
				o.usage.Model = body.Model
			}
			if body.Usage != nil {
				o.applyAnthropicUsage(body.Usage, true)
			}
		}
		return
	}
	var body struct {
		Model string       `json:"model"`
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.Model != "" {
			o.usage.Model = body.Model
		}
		if body.Usage != nil {
			o.applyOpenAIUsage(body.Usage)
		}
	}
}

// usageReadCloser wraps an upstream response body, counting decoded bytes and
// feeding a usageObserver when countUsage is true.
type usageReadCloser struct {
	rc       io.ReadCloser
	observer *usageObserver
	down     int64
	once     sync.Once
	onDone   func(down int64, usage *TokenUsage)
}

func newUsageReadCloser(rc io.ReadCloser, contentEncoding string, obs *usageObserver, onDone func(int64, *TokenUsage)) (io.ReadCloser, bool) {
	decoded := rc
	decompressed := false
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "gzip":
		if gr, err := gzip.NewReader(rc); err == nil {
			decoded = &stackedCloser{Reader: gr, closers: []io.Closer{gr, rc}}
			decompressed = true
		}
	case "deflate":
		fr := flate.NewReader(rc)
		decoded = &stackedCloser{Reader: fr, closers: []io.Closer{fr, rc}}
		decompressed = true
	}
	return &usageReadCloser{rc: decoded, observer: obs, onDone: onDone}, decompressed
}

type stackedCloser struct {
	io.Reader
	closers []io.Closer
}

func (s *stackedCloser) Close() error {
	var first error
	for _, c := range s.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (u *usageReadCloser) Read(p []byte) (int, error) {
	n, err := u.rc.Read(p)
	if n > 0 {
		u.down += int64(n)
		if u.observer != nil {
			u.observer.Write(p[:n])
		}
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			u.finish()
		}
		return n, err //nolint:wrapcheck // callers expect raw io.EOF and underlying stream errors
	}
	return n, nil
}

func (u *usageReadCloser) Close() error {
	err := u.rc.Close()
	u.finish()
	return err //nolint:wrapcheck // pass through underlying body close error
}

func (u *usageReadCloser) finish() {
	u.once.Do(func() {
		var usage *TokenUsage
		if u.observer != nil {
			res := u.observer.Finish()
			usage = &res
		}
		if u.onDone != nil {
			u.onDone(u.down, usage)
		}
	})
}

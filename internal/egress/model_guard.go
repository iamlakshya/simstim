package egress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

const (
	// WireAnthropicMessages identifies the Anthropic Messages API wire format.
	WireAnthropicMessages = "anthropic-messages"
	// WireOpenAIResponses identifies the OpenAI Responses API wire format.
	WireOpenAIResponses = "openai-responses"
	// WireOpenAIChat identifies the OpenAI Chat Completions wire format.
	WireOpenAIChat = "openai-chat"

	// maxDecodedBodyBytes caps decoded model request bodies at 64 MiB (spec 7.2).
	maxDecodedBodyBytes = 64 << 20
)

// Tool grant names recognised by --tools and [network] model_tools (spec 7.2).
const (
	ToolGrantWebFetch      = "web_fetch"
	ToolGrantWebSearch     = "web_search"
	ToolGrantCodeExecution = "code_execution"
	ToolGrantMCP           = "mcp"
	ToolGrantURLContent    = "url_content"
	ToolGrantFiles         = "files"
)

// ValidToolGrant reports whether name is one of the six spec 7.2 tool grant names.
func ValidToolGrant(name string) bool {
	switch name {
	case ToolGrantWebFetch, ToolGrantWebSearch, ToolGrantCodeExecution,
		ToolGrantMCP, ToolGrantURLContent, ToolGrantFiles:
		return true
	default:
		return false
	}
}

// guardRefusal describes one blocked model API request.
type guardRefusal struct {
	what   string // short label, e.g. "web_fetch_20260318" or "POST /v1/messages/batches"
	grant  string // --tools grant name if grantable, or ""
	why    string // short reason for audit logs
	isPath bool   // true when the refusal is for an unlisted HTTP method/path
}

func (g *guardRefusal) message(session, host string) string {
	prefix := brand.Name + ": "
	if session != "" {
		prefix += session + " "
	}
	if g.grant != "" {
		hint := fmt.Sprintf("%s policy allow %s --tools %s", brand.Name, host, g.grant)
		if session != "" {
			hint += " --in " + session
		}
		return fmt.Sprintf("%smay not use %s at %s. Try: %s", prefix, g.what, host, hint)
	}
	if g.isPath {
		return fmt.Sprintf("%smay not call %s at %s. Try: use one of the model API paths allowed by policy", prefix, g.what, host)
	}
	return fmt.Sprintf("%smay not use %s at %s. Try: remove that tool from the request or check the agent's model guard", prefix, g.what, host)
}

// cleanUpstreamPath validates rawPath against traversal or encoded separator
// smuggling and returns its canonical clean path.
func cleanUpstreamPath(rawPath, escapedPath string) (string, error) {
	lower := strings.ToLower(escapedPath)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%2e") {
		return "", errors.New("encoded path separator or dot segment")
	}
	if strings.Contains(rawPath, "\\") || strings.Contains(rawPath, "//") {
		return "", errors.New("non-canonical path")
	}
	for seg := range strings.SplitSeq(rawPath, "/") {
		if seg == "." || seg == ".." {
			return "", errors.New("path traversal segment")
		}
	}
	cleaned := path.Clean("/" + rawPath)
	if cleaned != rawPath && "/"+rawPath != cleaned {
		return "", errors.New("non-canonical path")
	}
	return cleaned, nil
}

// checkModelPath verifies method and cleanPath against guard.Paths and guard.Files.
func checkModelPath(guard *agents.ModelGuard, method, cleanPath string, toolAllowed func(string) bool) *guardRefusal {
	if guard == nil {
		return nil
	}
	target := strings.ToUpper(method) + " " + cleanPath
	if slices.Contains(guard.Paths, target) {
		return nil
	}
	for _, f := range guard.Files {
		if f == target || matchFileEndpoint(f, method, cleanPath) {
			if toolAllowed != nil && toolAllowed(ToolGrantFiles) {
				return nil
			}
			return &guardRefusal{what: "file uploads (" + target + ")", grant: ToolGrantFiles, why: "file upload refused"}
		}
	}
	return &guardRefusal{what: target, why: "path not on model allowlist", isPath: true}
}

func matchFileEndpoint(specEntry, method, cleanPath string) bool {
	parts := strings.Fields(specEntry)
	if len(parts) != 2 || !strings.EqualFold(parts[0], method) {
		return false
	}
	p := parts[1]
	if i := strings.IndexByte(p, '/'); i > 0 {
		// Allow "POST api.anthropic.com/v1/files" as written in spec 8.6.
		p = p[i:]
	}
	return p == cleanPath
}

// decodeRequestBody reads and, if gzip or deflate encoded, decompresses the
// request body up to maxDecodedBodyBytes (64 MiB, spec 7.2). Unknown encodings
// or payloads exceeding the cap fail closed.
func decodeRequestBody(body io.ReadCloser, contentEncoding string) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer func() { _ = body.Close() }()

	enc := strings.ToLower(strings.TrimSpace(contentEncoding))
	var reader io.Reader = body
	switch enc {
	case "", "identity":
	case "gzip":
		gr, err := gzip.NewReader(io.LimitReader(body, maxDecodedBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip request body: %w", err)
		}
		defer func() { _ = gr.Close() }()
		reader = gr
	case "deflate":
		fr := flate.NewReader(io.LimitReader(body, maxDecodedBodyBytes+1))
		defer func() { _ = fr.Close() }()
		reader = fr
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", contentEncoding)
	}

	raw, err := io.ReadAll(io.LimitReader(reader, maxDecodedBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(raw) > maxDecodedBodyBytes {
		return nil, fmt.Errorf("decoded request body exceeds %d bytes", maxDecodedBodyBytes)
	}
	return raw, nil
}

// checkModelBody inspects a decoded JSON request body for disallowed tool types,
// provider-side MCP servers, URL-fetched images/documents, and web search under
// the locked preset (spec 7.2). Unknown tool types fail closed.
func checkModelBody(guard *agents.ModelGuard, preset Preset, body []byte, toolAllowed func(string) bool) *guardRefusal {
	if guard == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return &guardRefusal{what: "malformed JSON request", why: "invalid JSON body"}
	}
	if root == nil {
		return &guardRefusal{what: "non-object JSON request", why: "JSON body must be an object"}
	}
	allow := func(grant string) bool {
		return toolAllowed != nil && toolAllowed(grant)
	}

	switch guard.Wire {
	case WireAnthropicMessages:
		return checkAnthropicBody(guard, preset, root, allow)
	case WireOpenAIResponses, WireOpenAIChat:
		return checkOpenAIBody(guard, preset, root, allow)
	default:
		return &guardRefusal{what: "unknown wire format " + guard.Wire, why: "unsupported model guard wire format"}
	}
}

func checkAnthropicBody(guard *agents.ModelGuard, preset Preset, root map[string]any, allow func(string) bool) *guardRefusal {
	if mcp, exists := root["mcp_servers"]; exists && !isEmptyJSON(mcp) {
		if !allow(ToolGrantMCP) {
			return &guardRefusal{what: "mcp_servers", grant: ToolGrantMCP, why: "provider MCP servers refused"}
		}
	}
	if ref := checkToolsList(guard, preset, root["tools"], classifyAnthropicTool, allow); ref != nil {
		return ref
	}
	if hasAnthropicURLSource(root) && !allow(ToolGrantURLContent) {
		return &guardRefusal{what: "URL-fetched image or document content", grant: ToolGrantURLContent, why: "URL content source refused"}
	}
	return nil
}

func checkOpenAIBody(guard *agents.ModelGuard, preset Preset, root map[string]any, allow func(string) bool) *guardRefusal {
	if ref := checkToolsList(guard, preset, root["tools"], classifyOpenAITool, allow); ref != nil {
		return ref
	}
	if hasOpenAIURLContent(root) && !allow(ToolGrantURLContent) {
		return &guardRefusal{what: "URL-fetched image or file content", grant: ToolGrantURLContent, why: "URL content source refused"}
	}
	return nil
}

func checkToolsList(
	guard *agents.ModelGuard,
	preset Preset,
	rawTools any,
	classify func(string) string,
	allow func(string) bool,
) *guardRefusal {
	return checkToolsListDepth(guard, preset, rawTools, classify, allow, 0)
}

const maxToolNestDepth = 8

func checkToolsListDepth(
	guard *agents.ModelGuard,
	preset Preset,
	rawTools any,
	classify func(string) string,
	allow func(string) bool,
	depth int,
) *guardRefusal {
	if rawTools == nil {
		return nil
	}
	if depth > maxToolNestDepth {
		return &guardRefusal{what: "deeply nested tools", why: "tool nesting limit exceeded"}
	}
	list, ok := rawTools.([]any)
	if !ok {
		return &guardRefusal{what: "non-array tools field", why: "invalid tools field"}
	}
	for _, entry := range list {
		if ref := checkToolEntry(guard, preset, entry, classify, allow, depth); ref != nil {
			return ref
		}
	}
	return nil
}

func checkToolEntry(
	guard *agents.ModelGuard,
	preset Preset,
	entry any,
	classify func(string) string,
	allow func(string) bool,
	depth int,
) *guardRefusal {
	obj, ok := entry.(map[string]any)
	if !ok {
		return &guardRefusal{what: "non-object tool entry", why: "invalid tool entry"}
	}
	rawType, hasType := obj["type"]
	if !hasType || rawType == nil {
		return nil // client tool with no type passes (spec 7.2)
	}
	toolType, ok := rawType.(string)
	if !ok {
		return &guardRefusal{what: "non-string tool type", why: "invalid tool type"}
	}
	if toolType == "" {
		return nil
	}
	if slices.Contains(guard.Tools, toolType) {
		// OpenAI Responses "namespace" groups nested client tools; validate
		// its nested tools recursively so forbidden types cannot hide inside.
		if toolType == "namespace" {
			return checkToolsListDepth(guard, preset, obj["tools"], classify, allow, depth+1)
		}
		return nil
	}
	if slices.Contains(guard.Search, toolType) {
		if preset != PresetLocked || allow(ToolGrantWebSearch) {
			return nil
		}
		return &guardRefusal{what: toolType, grant: ToolGrantWebSearch, why: "web search refused under locked preset"}
	}
	grant := classify(toolType)
	if grant != "" && allow(grant) {
		return nil
	}
	return &guardRefusal{what: toolType, grant: grant, why: "disallowed model tool type " + toolType}
}

func classifyAnthropicTool(toolType string) string {
	switch {
	case strings.HasPrefix(toolType, "web_fetch"):
		return ToolGrantWebFetch
	case strings.HasPrefix(toolType, "web_search"):
		return ToolGrantWebSearch
	case strings.HasPrefix(toolType, "code_execution"):
		return ToolGrantCodeExecution
	case toolType == "mcp_toolset":
		return ToolGrantMCP
	default:
		return ""
	}
}

func classifyOpenAITool(toolType string) string {
	switch {
	case toolType == "mcp":
		return ToolGrantMCP
	case toolType == "code_interpreter":
		return ToolGrantCodeExecution
	case strings.HasPrefix(toolType, "web_search"):
		return ToolGrantWebSearch
	default:
		return ""
	}
}

func isEmptyJSON(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.([]any); ok {
		return len(s) == 0
	}
	return false
}

// hasAnthropicURLSource walks v looking for image or document blocks with
// source.type == "url" anywhere in messages, system prompts or tool results.
func hasAnthropicURLSource(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "image" || typ == "document" {
			if src, ok := x["source"].(map[string]any); ok {
				if st, _ := src["type"].(string); strings.EqualFold(st, "url") {
					return true
				}
			}
		}
		for _, child := range x {
			if hasAnthropicURLSource(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if hasAnthropicURLSource(child) {
				return true
			}
		}
	}
	return false
}

// hasOpenAIURLContent walks v looking for input_image.image_url, input_file.file_url
// or Chat Completions image_url.url pointing to an http:// or https:// URL.
// data: URLs and file IDs pass.
func hasOpenAIURLContent(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "input_image" || typ == "image_url" {
			if isHTTPURLValue(x["image_url"]) {
				return true
			}
		}
		if typ, _ := x["type"].(string); typ == "input_file" {
			if isHTTPURLValue(x["file_url"]) {
				return true
			}
		}
		for _, child := range x {
			if hasOpenAIURLContent(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if hasOpenAIURLContent(child) {
				return true
			}
		}
	}
	return false
}

func isHTTPURLValue(v any) bool {
	switch u := v.(type) {
	case string:
		low := strings.ToLower(strings.TrimSpace(u))
		return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
	case map[string]any:
		if s, ok := u["url"].(string); ok {
			return isHTTPURLValue(s)
		}
	}
	return false
}

// writeProviderError writes an API-shaped JSON refusal with x-should-retry: false
// and no retry-after header so neither Anthropic nor OpenAI SDKs retry it (spec 7.2).
func writeProviderError(w http.ResponseWriter, wire string, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-should-retry", "false")
	w.Header().Del("Retry-After")
	w.WriteHeader(status)

	if wire == WireAnthropicMessages {
		payload := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errType,
				"message": message,
			},
			"request_id": "req_" + randomHex(12),
		}
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   nil,
			"code":    nil,
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func providerForbiddenType(wire string) string {
	if wire == WireAnthropicMessages {
		return "permission_error"
	}
	return "request_forbidden"
}

func providerAuthErrorType(wire string) string {
	if wire == WireAnthropicMessages {
		return "authentication_error"
	}
	return "invalid_api_key"
}

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "000000000000000000000000"
	}
	return hex.EncodeToString(buf)
}

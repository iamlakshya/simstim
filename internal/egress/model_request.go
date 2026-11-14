package egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// A request keeps only public capability names after inspection. Its body is
// replayed byte for byte, never logged or retained by the policy engine.
type modelRequest struct {
	request   Request
	guard     *agents.ModelGuard
	features  []string
	search    bool
	webSocket bool
	file      bool
	display   map[string]string
}

func (m *modelRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<model request redacted>")
}

func (m *modelRequest) name(feature, kind string) {
	if m.display == nil {
		m.display = make(map[string]string)
	}
	if len(kind) > 96 {
		return
	}
	for _, r := range kind {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return
		}
	}
	m.display[feature] = kind
}

func (m *modelRequest) require(feature string) {
	if !slices.Contains(m.features, feature) {
		m.features = append(m.features, feature)
	}
}

func (m *modelRequest) refusal(engine *Engine, permit *Permit, mode evalMode) string {
	if engine.modelGuard(m.request) == nil {
		return "model path"
	}
	features := slices.Clone(m.features)
	if m.search && engine.preset == api.PresetLocked && !slices.Contains(features, "web_search") {
		features = append(features, "web_search")
	}
	engine.ledger.mu.Lock()
	defer engine.ledger.mu.Unlock()
	if !engine.validPermit(permit, mode) {
		return "model policy"
	}
	check := mode
	if mode == evalReserve {
		check = evalPrecheck
	}
	if permit != nil && permit.committed && check == evalPrecheck {
		check = evalModelPrecheck
	}
	// All required names are checked and acquired under one ledger ownership.
	// Refusal cannot leave a partial set reserved on the connected permit.
	for _, feature := range features {
		if !engine.modelToolAllowedLocked(m.request, feature, permit, check) {
			return feature
		}
	}
	if mode == evalReserve {
		reserve := evalReserve
		if permit != nil && permit.committed {
			reserve = evalModelReserve
		}
		for _, feature := range features {
			engine.modelToolAllowedLocked(m.request, feature, permit, reserve)
		}
	}
	return ""
}

func (m *modelRequest) inspect(body []byte, websocket bool) error {
	// The standard scanner enforces its nesting limit before recursive token
	// inspection; Token alone does not bound a stream of opening delimiters.
	if !utf8.Valid(body) || !json.Valid(body) {
		return errors.New("invalid model JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	scan := modelJSONScan{decoder: decoder, request: m}
	root, err := scan.value(false, false, 0)
	if err != nil || root.kind != '{' {
		return errors.New("invalid model JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing model JSON")
	}
	if websocket && root.typeName != "response.create" {
		return errors.New("unsupported model WebSocket event")
	}
	return nil
}

type modelJSONValue struct {
	kind      byte
	text      string
	typeName  string
	remote    bool
	sourceURL bool
	nonempty  bool
}

type modelJSONScan struct {
	decoder *json.Decoder
	request *modelRequest
}

// Token traversal does not materialise a second JSON tree. Object keys are
// checked for duplicates so an upstream decoder cannot choose another type or
// input than the one checked here. Nesting uses encoding/json's own limit.
func (s *modelJSONScan) value(content, tool bool, depth int) (modelJSONValue, error) {
	token, err := s.decoder.Token()
	if err != nil {
		return modelJSONValue{}, err
	}
	switch value := token.(type) {
	case string:
		return modelJSONValue{kind: 's', text: value, remote: modelRemoteURL(value), nonempty: value != ""}, nil
	case json.Delim:
		if value == '[' {
			result := modelJSONValue{kind: '['}
			for s.decoder.More() {
				result.nonempty = true
				if _, err := s.value(content, tool, depth+1); err != nil {
					return modelJSONValue{}, err
				}
			}
			_, err := s.decoder.Token()
			return result, err
		}
		if value != '{' {
			return modelJSONValue{}, errors.New("invalid model JSON delimiter")
		}
		result := modelJSONValue{kind: '{'}
		seen := make(map[string]bool)
		var sourceURL, imageURL, fileURL, hasType, invalidType bool
		for s.decoder.More() {
			keyToken, err := s.decoder.Token()
			if err != nil {
				return modelJSONValue{}, err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return modelJSONValue{}, errors.New("ambiguous model JSON key")
			}
			seen[key], result.nonempty = true, true
			child, err := s.value(content || depth == 0 && (key == "messages" || key == "input"), depth == 0 && key == "tools", depth+1)
			if err != nil {
				return modelJSONValue{}, err
			}
			switch key {
			case "type":
				hasType = true
				invalidType = child.kind != 's'
				if !invalidType {
					result.typeName = child.text
				}
			case "source":
				sourceURL = child.sourceURL
			case "image_url":
				imageURL = child.remote
			case "file_url":
				fileURL = child.remote
			case "url":
				result.remote = child.remote
			case "mcp_servers":
				if depth == 0 && child.nonempty {
					s.request.require("mcp")
					s.request.name("mcp", "mcp_servers")
				}
			}
		}
		if _, err := s.decoder.Token(); err != nil {
			return modelJSONValue{}, err
		}
		result.sourceURL = result.typeName == "url"
		if content && ((result.typeName == "image" || result.typeName == "document") && sourceURL ||
			(result.typeName == "input_image" || result.typeName == "image_url") && imageURL || result.typeName == "input_file" && fileURL) {
			s.request.require("url_content")
		}
		if tool && hasType {
			if invalidType {
				return modelJSONValue{}, errors.New("invalid model tool type")
			}
			switch {
			case slices.Contains(s.request.guard.Tools, result.typeName):
			case slices.Contains(s.request.guard.Search, result.typeName):
				s.request.search = true
				s.request.name("web_search", result.typeName)
			default:
				capability := modelToolCapability(s.request.guard.Wire, result.typeName)
				if capability == "" {
					s.request.name("model request type", result.typeName)
					return modelJSONValue{}, errors.New("unlisted model tool type")
				}
				s.request.require(capability)
				s.request.name(capability, result.typeName)
			}
		}
		return result, nil
	default:
		return modelJSONValue{nonempty: token != nil}, nil
	}
}

func modelRemoteURL(value string) bool {
	scheme, _, found := strings.Cut(strings.TrimSpace(value), ":")
	return found && (strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https"))
}

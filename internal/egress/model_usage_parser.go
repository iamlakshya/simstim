package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

type modelUsageParserV1 struct {
	ctx      context.Context
	wire     string
	mode     ModelUsageModeV1
	buffer   []byte
	event    []byte
	fields   modelUsageFieldsV1
	has      bool
	terminal bool
	bad      bool
	depth    int
	inString bool
	escaped  bool
}

func newModelUsageParserV1(wire string) *modelUsageParserV1 {
	return &modelUsageParserV1{ctx: context.Background(), wire: wire, mode: ModelUsageJSONV1}
}

func (parser *modelUsageParserV1) setMode(mode ModelUsageModeV1) {
	if len(parser.buffer) != 0 || len(parser.event) != 0 || parser.has {
		parser.bad = true
		return
	}
	if mode != ModelUsageJSONV1 && mode != ModelUsageSSEV1 && mode != ModelUsageWebSocketV1 {
		parser.bad = true
		return
	}
	parser.mode = mode
}

func (parser *modelUsageParserV1) appendBuffer(target *[]byte, value []byte) bool {
	if len(value) > int(maxDecodedHTTPBody)-len(*target) {
		parser.bad = true
		clear(*target)
		*target = nil
		return false
	}
	*target = append(*target, value...)
	return true
}

func (parser *modelUsageParserV1) feed(payload []byte) {
	if parser.bad {
		return
	}
	switch parser.mode {
	case ModelUsageJSONV1:
		parser.jsonBytes(payload)
	case ModelUsageSSEV1:
		for len(payload) > 0 {
			end := bytes.IndexByte(payload, '\n')
			if end < 0 {
				parser.appendBuffer(&parser.buffer, payload)
				return
			}
			if !parser.appendBuffer(&parser.buffer, payload[:end]) {
				return
			}
			parser.sseLine()
			payload = payload[end+1:]
		}
	case ModelUsageWebSocketV1:
		parser.webSocketBytes(payload)
	}
}

func (parser *modelUsageParserV1) sseLine() {
	line := bytes.TrimSuffix(parser.buffer, []byte{'\r'})
	if len(line) == 0 {
		parser.sseEvent()
	} else if bytes.HasPrefix(line, []byte("data:")) {
		value := bytes.TrimPrefix(line[5:], []byte{' '})
		if len(parser.event) > 0 && !parser.appendBuffer(&parser.event, []byte{'\n'}) {
			return
		}
		parser.appendBuffer(&parser.event, value)
	}
	clear(parser.buffer)
	parser.buffer = parser.buffer[:0]
}

func (parser *modelUsageParserV1) sseEvent() {
	if len(parser.event) == 0 {
		return
	}
	if bytes.Equal(bytes.TrimSpace(parser.event), []byte("[DONE]")) {
		parser.terminal = true
	} else {
		parser.recordEvent(parser.event, false)
	}
	clear(parser.event)
	parser.event = parser.event[:0]
}

func (parser *modelUsageParserV1) webSocketBytes(payload []byte) {
	for _, value := range payload {
		if len(parser.buffer) == 0 && (value == ' ' || value == '\n' || value == '\r' || value == '\t') {
			continue
		}
		if !parser.appendBuffer(&parser.buffer, []byte{value}) {
			return
		}
		if parser.inString {
			if parser.escaped {
				parser.escaped = false
			} else if value == '\\' {
				parser.escaped = true
			} else if value == '"' {
				parser.inString = false
			}
			continue
		}
		switch value {
		case '"':
			parser.inString = true
		case '{', '[':
			parser.depth++
		case '}', ']':
			parser.depth--
			if parser.depth == 0 {
				parser.recordEvent(parser.buffer, false)
				clear(parser.buffer)
				parser.buffer = parser.buffer[:0]
			}
		}
		if parser.depth < 0 {
			parser.bad = true
			return
		}
	}
}

func (parser *modelUsageParserV1) recordEvent(raw []byte, plain bool) {
	if parser.terminal {
		return
	}
	event, err := parseModelUsageEventContextV1(parser.ctx, raw)
	if err != nil {
		parser.bad = true
		return
	}
	parser.recordParsedEvent(event, plain)
}

func (parser *modelUsageParserV1) recordParsedEvent(event modelUsageEventV1, plain bool) {
	if event.badUsage {
		parser.bad = true
		return
	}
	if parser.terminal {
		return
	}
	switch parser.wire {
	case "anthropic-messages":
		if event.has && (plain || event.kind == "message_start" || event.kind == "message_delta") {
			parser.replaceAnthropic(event.usage)
		}
		if plain || event.kind == "message_stop" {
			parser.terminal = true
		}
	case "openai-responses":
		terminal := event.kind == "response.completed" || event.kind == "response.incomplete" || event.kind == "response.failed"
		if plain {
			terminal = event.status == "" || event.status == "completed" || event.status == "incomplete" || event.status == "failed"
		}
		if terminal {
			parser.fields, parser.has, parser.terminal = event.usage, event.has, true
		}
	case "openai-chat-completions":
		if event.has {
			parser.fields, parser.has = event.usage, true
		}
		if plain {
			parser.terminal = true
		}
	}
}

func (parser *modelUsageParserV1) replaceAnthropic(current modelUsageFieldsV1) {
	replace := func(target *modelUsageNumberV1, next modelUsageNumberV1) {
		if next.present {
			*target = next
		}
	}
	replace(&parser.fields.Input, current.Input)
	replace(&parser.fields.Output, current.Output)
	replace(&parser.fields.CacheWrite, current.CacheWrite)
	replace(&parser.fields.CacheRead, current.CacheRead)
	replace(&parser.fields.ServerTools.Search, current.ServerTools.Search)
	parser.has = parser.has || current.Input.present || current.Output.present || current.CacheWrite.present || current.CacheRead.present
}

func (parser *modelUsageParserV1) finish(complete bool) (api.ModelUsageCountsV1, bool, bool) {
	if !parser.bad && parser.mode == ModelUsageJSONV1 {
		if !utf8.Valid(parser.buffer) || !json.Valid(parser.buffer) {
			parser.bad = true
		} else {
			parser.recordEvent(parser.buffer, true)
		}
	} else if !parser.bad && parser.mode == ModelUsageSSEV1 {
		if len(parser.buffer) > 0 {
			parser.sseLine()
		}
		parser.sseEvent()
	}
	clear(parser.buffer)
	clear(parser.event)
	parser.buffer, parser.event = nil, nil
	if !complete || parser.bad || !parser.terminal || !parser.has {
		return api.ModelUsageCountsV1{}, false, false
	}
	return parser.reportedCounts()
}

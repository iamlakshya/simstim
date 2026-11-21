package egress

// A complete JSON object may arrive before the body EOF or forwarding fence.
// Discover its reported counters once, while retaining the original bytes for
// final whole-answer validation. Later trailing data cannot become reported.
func (parser *modelUsageParserV1) jsonBytes(payload []byte) {
	start := len(parser.buffer)
	if !parser.appendBuffer(&parser.buffer, payload) {
		return
	}
	for index, value := range payload {
		if parser.terminal {
			continue
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
				parser.recordEvent(parser.buffer[:start+index+1], true)
			}
		}
		if parser.depth < 0 {
			parser.bad = true
			return
		}
	}
}

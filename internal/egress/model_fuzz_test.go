package egress

import (
	"bytes"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func FuzzModelRequestToolAndNestedURLGuard(f *testing.F) {
	engine, err := NewEngine("codex", api.Policy{Preset: api.PresetBalanced})
	if err != nil {
		f.Fatal(err)
	}
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	guard := engine.modelGuard(request)
	for _, seed := range [][2]string{{"function", "data:image/png;base64,AA=="}, {"mcp", "https://example.com/image"}, {"file_search", "data:x"}, {"web_search", "HTTPS://example.com/image"}, {"function", "https://example.com/image"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, tool, image string) {
		if len(tool)+len(image) > 8<<10 {
			t.Skip()
		} // Bounds this test's generated fixture, not admission.
		body, err := json.Marshal(map[string]any{
			"tools": []any{map[string]any{"type": tool}},
			"input": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "input_image", "image_url": image}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		model := &modelRequest{request: request, guard: guard}
		parseErr := model.inspect(body, false)
		allowed := parseErr == nil && model.refusal(engine, nil, evalPrecheck) == ""
		listed := slices.Contains(guard.Tools, tool) || slices.Contains(guard.Search, tool)
		parsed, urlErr := url.Parse(strings.TrimSpace(image))
		remote := urlErr == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https"))
		if allowed && (!listed || remote) {
			t.Fatal("ungranted tool or nested provider URL passed the model guard")
		}
		if tool == "function" && image == "data:image/png;base64,AA==" && !allowed {
			t.Fatal("local function and data URL were refused")
		}
	})
}

func FuzzModelSharedWebSocketFrameReader(f *testing.F) {
	f.Add(wsTestWire(1, true, true, []byte(`{"type":"response.create","input":[]}`)), true)
	f.Add([]byte{0xc1, 0x80, 0, 0, 0, 0}, true)
	f.Add([]byte{0x82, 0x7f, 0, 0, 0, 0, 0, 0, 0, 1}, false)
	f.Fuzz(func(t *testing.T, wire []byte, masked bool) {
		if len(wire) > 128<<10 {
			t.Skip()
		}
		input := bytes.NewReader(wire)
		frame, err := readWebSocketFrame(input, masked)
		if err != nil {
			return
		}
		if len(wire) < 2 || wire[0]&0x70 != 0 || (wire[1]&0x80 != 0) != masked {
			t.Fatal("compressed, reserved or unmasked client frame passed")
		}
		var scratch [256]byte
		for frame.remaining != 0 {
			if _, err := frame.read(input, scratch[:]); err != nil {
				return
			}
		}
	})
}

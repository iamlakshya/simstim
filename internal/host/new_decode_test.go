//go:build !windows

package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNewDecodePreservesAnAbsentOrExplicitUnwalledRequest(t *testing.T) {
	for _, raw := range []string{`{}`, `{"wall":""}`, `{"wall":"none"}`, `{"\u0077all":"none"}`} {
		args, err := decodeNewArgs(json.RawMessage(raw))
		if err != nil || args.Wall != "" && args.Wall != "none" {
			t.Fatalf("valid unwalled request %s became %+v, %v", raw, args, err)
		}
	}
}

func TestNewBridgeRefusesAmbiguousOrUnsupportedWallsBeforeCreation(t *testing.T) {
	for raw, code := range map[string]string{
		`null`:                                  "bad_request",
		`{"\u0077all":"none","future":{"x":1}}`: "bad_request",
		`{"wall":null}`:                         "bad_wall",
		`{"wall":1}`:                            "bad_wall",
		`{"wall":false}`:                        "bad_wall",
		`{"wall":{}}`:                           "bad_wall",
		`{"wall":[]}`:                           "bad_wall",
		`{"wall":"unknown"}`:                    "bad_wall",
		`{"wall":"auto"}`:                       "wall_unavailable",
		`{"wall":"process","floor":"microvm"}`:  "wall_floor",
		`{"wall":"gvisor"}`:                     "wall_unavailable",
		`{"wall":"microvm"}`:                    "wall_unavailable",
		`{"wall":"process","wall":null}`:        "bad_request",
		`{"wall":"process","wall":"none"}`:      "bad_request",
		`{"wall":"process","\u0077all":"none"}`: "bad_request",
		`{"Wall":"none"}`:                       "bad_request",
		`{"wall":"process","WALL":"none"}`:      "bad_request",
	} {
		t.Run(raw, func(t *testing.T) {
			agentBridgeEnv(t)
			var output bytes.Buffer
			b := &bridge{ctx: t.Context(), w: proto.NewWriter(&output)}
			b.handle(proto.Request{ID: 1, Op: "new", Args: json.RawMessage(raw)})
			frame, err := proto.Read(bufio.NewReader(&output))
			if err != nil || frame.Type != proto.TResponse {
				t.Fatalf("new refusal frame=%+v error=%v", frame, err)
			}
			var response proto.Response
			if err := json.Unmarshal(frame.Payload, &response); err != nil || response.OK || response.Error == nil || response.Error.Code != code || response.Error.Hint == "" {
				t.Fatalf("new refusal=%+v error=%v", response, err)
			}
			if _, err := os.Stat(paths.SessionsDir()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused wall request created session state: %v", err)
			}
		})
	}
}

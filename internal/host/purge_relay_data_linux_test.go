//go:build linux

package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeRelayCannotCreateVectorOrChangeTargetV3(t *testing.T) {
	args := api.PurgeArgs{HostID: strings.Repeat("a", 64), Nonce: strings.Repeat("b", 64)}
	for _, operation := range []string{purgeBeginV3, purgeContinueV3} {
		file, err := os.CreateTemp(t.TempDir(), "input")
		if err != nil {
			t.Fatal(err)
		}
		request, err := purgeRequestFrameV3(7, operation, args)
		if err != nil {
			t.Fatal(err)
		}
		if err := proto.NewWriter(file).WriteJSON(proto.TRequest, request); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		_, got, err := readRawPurgeRelayRequestV3(file)
		_ = file.Close()
		if operation == purgeBeginV3 && err == nil {
			t.Fatal("fresh relay accepted Begin as a new vector constructor")
		}
		if operation == purgeContinueV3 && (err != nil || got.Args != args) {
			t.Fatalf("fixed continuation changed: %v %#v", err, got)
		}
	}
	result := api.PurgeResult{HostID: args.HostID, Nonce: args.Nonce}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	frame := func(id uint64, result json.RawMessage) proto.Frame {
		raw, err := json.Marshal(proto.Response{ID: id, OK: true, Result: result})
		if err != nil {
			t.Fatal(err)
		}
		return proto.Frame{Type: proto.TResponse, Payload: raw}
	}
	if _, err := decodePurgeRelayResponseDataV3(frame(7, raw), 7, args); err != nil {
		t.Fatal(err)
	}
	if _, err := decodePurgeRelayResponseDataV3(frame(8, raw), 7, args); err == nil {
		t.Fatal("another response identifier satisfied the original request")
	}
	other := args
	other.HostID = strings.Repeat("c", 64)
	if _, err := decodePurgeRelayResponseDataV3(frame(7, raw), 7, other); err == nil {
		t.Fatal("another host satisfied the original target")
	}
	other = args
	other.Nonce = strings.Repeat("d", 64)
	if _, err := decodePurgeRelayResponseDataV3(frame(7, raw), 7, other); err == nil {
		t.Fatal("another vector satisfied the original removal nonce")
	}
}

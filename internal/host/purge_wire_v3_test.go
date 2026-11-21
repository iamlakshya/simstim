//go:build linux

package host

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeClosedRequestRefusesAuthorityFieldsV3(t *testing.T) {
	args := api.PurgeArgs{HostID: strings.Repeat("a", 64), Nonce: strings.Repeat("b", 64), Force: true}
	request, err := purgeRequestFrameV3(9, purgeContinueV3, args)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	frame := proto.Frame{Type: proto.TRequest, Payload: raw}
	got, data, err := decodePurgeRequestDataV3(frame)
	if err != nil || got.ID != 9 || data.Args != args {
		t.Fatalf("fixed request: %v, %#v, %#v", err, got, data)
	}
	for _, field := range []string{
		`"source":{"stopped":true}`,
		`"operation":{"owned":true}`,
		`"roots":["/tmp"]`,
		`"phase":"stopped"`,
		`"Version":3`,
		`"v":3`,
	} {
		injected := strings.Replace(string(raw), `"v":3`, `"v":3,`+field, 1)
		if _, _, err := decodePurgeRequestDataV3(proto.Frame{Type: proto.TRequest, Payload: []byte(injected)}); err == nil {
			t.Fatalf("accepted injected authority/data alias: %s", field)
		}
	}
	for _, mutation := range []proto.Request{
		{ID: 0, Op: request.Op, Args: request.Args},
		{ID: 9, Op: "session.stop", Args: request.Args},
		{ID: 9, Op: request.Op, Args: request.Args, NewAttempt: strings.Repeat("c", 64)},
	} {
		raw, err := json.Marshal(mutation)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := decodePurgeRequestDataV3(proto.Frame{Type: proto.TRequest, Payload: raw}); err == nil {
			t.Fatalf("accepted another operation's metadata: %#v", mutation)
		}
	}
	frame.Type = proto.TResponse
	if _, _, err := decodePurgeRequestDataV3(frame); err == nil {
		t.Fatal("accepted response as request")
	}
}

func TestPurgeSocketDisconnectCancelsOnlyRequestV3(t *testing.T) {
	if err := CheckCaller(); err != nil {
		t.Skipf("native outside caller unavailable: %v", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "request.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewNativeStartupLineV1(server)
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	native, err := pinUnverifiedNativeHelperPeerV1(server)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	identity, err := currentNativeHelperIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := identity.executable.Stat()
	if err != nil {
		t.Fatal(err)
	}
	identity.expected, native.identity = expected, identity
	peer := &purgeRequestPeerV3{native: native}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := peer.Check(lifetime); err != nil {
		t.Skipf("native placement unavailable: %v", err)
	}
	request, join, err := purgeRequestLifetimeV3(lifetime, line, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer join()
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-request.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("original socket half-close did not cancel its request")
	}
	if lifetime.Err() != nil {
		t.Fatal("transport cancellation ended the independent owner lifetime")
	}
}

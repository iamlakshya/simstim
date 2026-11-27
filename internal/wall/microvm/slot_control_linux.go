//go:build linux

package microvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"
)

type slotControlServer struct {
	mu         sync.Mutex
	connection net.Conn
	encoder    *json.Encoder
	source     *slotlaunch.NativeSourceV1
	proof      *slotNativeProof
	driver     string
}

func (s *slotControlServer) send(message runnerMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(message)
}
func (s *slotControlServer) serve(ctx context.Context) {
	if s.source.SameGuestV1(ctx, s.proof.guest) != nil {
		return
	}
	if s.send(runnerMessage{Kind: "ready", Session: s.proof.spec.ID, Driver: s.driver}) != nil {
		return
	}
	scanner := bufio.NewScanner(s.connection)
	scanner.Buffer(make([]byte, 1024), 4096)
	for scanner.Scan() {
		var request runnerRequest
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ID == 0 {
			return
		}
		reply := runnerMessage{Kind: "reply", ID: request.ID}
		state, err := slotVMMAPI(ctx, s.source, s.driver, request.Op)
		if err != nil {
			reply.Error = err.Error()
		} else {
			reply.State = state
		}
		if s.send(reply) != nil {
			return
		}
	}
}

func slotVMMAPI(ctx context.Context, source *slotlaunch.NativeSourceV1, driver, operation string) (string, error) {
	if operation != "state" && operation != "power-button" {
		return "", fmt.Errorf("the slot VMM API operation is not allowed")
	}
	if driver == "nitro" && operation == "power-button" {
		return "", fmt.Errorf("use the guest control stream to stop a Nitro Sandbox")
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if driver == "firecracker" || driver == "nitro" {
		connection, err := source.DialAPIV1(call)
		if err != nil {
			return "", err
		}
		client, err := fc.NewClient(connection)
		if err != nil {
			connection.Close()
			return "", err
		}
		defer client.Close()
		if operation == "state" {
			return client.State(call)
		}
		if runtime.GOARCH != "amd64" {
			return "", fmt.Errorf("use the guest control stream to stop this Firecracker VM")
		}
		return "", client.PowerButton(call)
	}
	method, path := http.MethodGet, "/api/v1/vm.info"
	if operation == "power-button" {
		method, path = http.MethodPut, "/api/v1/vm.power-button"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return source.DialAPIV1(ctx) }, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("the slot VMM API cannot redirect") }}
	request, err := http.NewRequestWithContext(call, method, "http://vmm"+path, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if operation == "power-button" {
		if response.StatusCode != http.StatusNoContent {
			return "", fmt.Errorf("the VMM power button returned HTTP %d", response.StatusCode)
		}
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the VMM state returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return "", fmt.Errorf("the VMM state exceeds its bound")
	}
	var state struct {
		State string `json:"state"`
	}
	if json.Unmarshal(data, &state) != nil || len(state.State) == 0 || len(state.State) > 64 {
		return "", fmt.Errorf("the VMM state is invalid")
	}
	return state.State, nil
}

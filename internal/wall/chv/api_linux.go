//go:build linux

package chv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *runnerServer) serve(ctx context.Context, socket string) {
	scanner := bufio.NewScanner(s.conn)
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var request runnerRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		reply := runnerMessage{Kind: "reply", ID: request.ID}
		if request.ID == 0 {
			reply.Error = "private VMM control needs a request ID"
		} else {
			state, err := vmmAPI(ctx, socket, request.Op)
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.State = state
			}
		}
		if err := s.send(reply); err != nil {
			return
		}
	}
}

func vmmAPI(ctx context.Context, socket, operation string) (string, error) {
	method, path := http.MethodGet, "/api/v1/vm.info"
	switch operation {
	case "state":
	case "power-button":
		method = http.MethodPut
		path = "/api/v1/vm.power-button"
	default:
		return "", fmt.Errorf("VMM API operation is not allowed")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("VMM API redirects are not allowed") }}
	request, err := http.NewRequestWithContext(requestCtx, method, "http://vmm"+path, nil)
	if err != nil {
		return "", fmt.Errorf("build VMM API request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("call private VMM API: %w", err)
	}
	defer response.Body.Close()
	if operation == "power-button" {
		if response.StatusCode != http.StatusNoContent {
			return "", fmt.Errorf("VMM power button returned HTTP%d", response.StatusCode)
		}
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("VMM state returned HTTP%d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, proto.MaxFrame+1))
	if err != nil {
		return "", fmt.Errorf("read VMM state: %w", err)
	}
	if len(data) > proto.MaxFrame {
		return "", fmt.Errorf("VMM state exceeds its bound")
	}
	var info struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("decode VMM state: %w", err)
	}
	if len(info.State) == 0 || len(info.State) > 64 {
		return "", fmt.Errorf("VMM state is missing or exceeds its bound")
	}
	return info.State, nil
}

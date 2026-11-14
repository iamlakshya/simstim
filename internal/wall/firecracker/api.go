// Package firecracker implements the Firecracker microvm backend.
package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// controlClient uses the runner's already connected and peer-checked Unix
// stream. It cannot select a socket path, use a proxy or reopen a connection.
// Checking that the peer belongs to the owned VMM remains the runner's job.
type controlClient struct {
	connection net.Conn
	transport  *http.Transport
	client     *http.Client
	operations chan struct{}
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
}

func newControlClient(connection net.Conn) (*controlClient, error) {
	if connection == nil {
		return nil, errors.New("the private VMM API needs its owned connection")
	}
	var dialMu sync.Mutex
	dialled := false
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if dialled {
				return nil, errors.New("the private VMM API connection cannot be reopened")
			}
			dialled = true
			return connection, nil
		},
		DisableCompression:     true,
		MaxConnsPerHost:        1,
		MaxIdleConnsPerHost:    1,
		MaxResponseHeaderBytes: proto.MaxFrame,
		ResponseHeaderTimeout:  5 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the private VMM API cannot redirect")
		},
	}
	return &controlClient{connection: connection, transport: transport, client: client, operations: make(chan struct{}, 1)}, nil
}

func (c *controlClient) state(ctx context.Context) (string, error) {
	data, err := c.request(ctx, http.MethodGet, "/", nil, http.StatusOK)
	if err != nil {
		return "", err
	}
	var info struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("decode private VMM state: %w", err)
	}
	switch info.State {
	case "Not started", "Running", "Paused":
		return info.State, nil
	default:
		return "", errors.New("the private VMM API returned an unknown state")
	}
}

// powerButton is available to the x86 runner only. Normal shutdown uses the
// guest's control stream on both architectures, before the owned kill path.
func (c *controlClient) powerButton(ctx context.Context) error {
	_, err := c.request(ctx, http.MethodPut, "/actions", []byte(`{"action_type":"SendCtrlAltDel"}`), http.StatusNoContent)
	return err
}

func (c *controlClient) request(ctx context.Context, method, path string, body []byte, status int) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c.operations <- struct{}{}:
	}
	defer func() { <-c.operations }()
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, "http://vmm"+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build private VMM request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call private VMM API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, proto.MaxFrame+1))
	if err != nil {
		return nil, fmt.Errorf("read private VMM response: %w", err)
	}
	if len(data) > proto.MaxFrame {
		return nil, errors.New("the private VMM response exceeds its frame bound")
	}
	if response.StatusCode != status {
		return nil, fmt.Errorf("the private VMM API returned HTTP %d", response.StatusCode)
	}
	return data, nil
}

func (c *controlClient) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeErr = c.connection.Close()
		if errors.Is(c.closeErr, net.ErrClosed) {
			c.closeErr = nil
		}
		c.transport.CloseIdleConnections()
	})
	return c.closeErr
}

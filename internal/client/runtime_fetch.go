package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

func fetchRuntimeHost(ctx context.Context, c *Conn, request runtimecache.RuntimeRequest, w io.Writer) (api.RuntimeStatus, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return api.RuntimeStatus{}, fmt.Errorf("name host runtime transfer: %w", err)
	}
	request.Transfer = hex.EncodeToString(nonce[:])
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	type response struct {
		status api.RuntimeStatus
		err    error
	}
	done := make(chan response, 1)
	started := time.Now()
	id, pending, err := c.startCall(operation, "runtime.fetch", request)
	if err != nil {
		return api.RuntimeStatus{}, err
	}
	go func() {
		var result api.RuntimeStatus
		err := c.awaitCall(operation, "runtime.fetch", id, pending, &result)
		done <- response{result, err}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	complete := false
	defer func() {
		if !complete {
			abortRuntime(ctx, c, request.Transfer)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return api.RuntimeStatus{}, ctx.Err()
		case value := <-done:
			if value.err != nil {
				return value.status, runtimeClientError(c.Host.Name, value.err)
			}
			complete = true
			status, err := runtimeReady(ctx, c, request)
			if err != nil {
				return status, err
			}
			for _, item := range request.Manifest.Items {
				runtimeLine(w, "%s: %d bytes in %s, sha256 ok\n", CleanLine(item.Name), item.Size, time.Since(started).Round(time.Millisecond))
			}
			return status, nil
		case <-ticker.C:
			status, err := runtimeStatus(operation, c, request)
			if err != nil {
				return status, err
			}
			for _, transfer := range status.Transfers {
				for _, item := range request.Manifest.Items {
					if item.SHA256 == transfer.SHA256 {
						runtimeLine(w, "%s: %d/%d bytes\n", CleanLine(item.Name), transfer.Received, item.Size)
						break
					}
				}
			}
		}
	}
}

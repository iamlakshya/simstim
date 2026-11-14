package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
)

func fetchImageHost(ctx context.Context, c *Conn, request image.Request, progress io.Writer) (image.Record, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return image.Record{}, err
	}
	request.Transfer = hex.EncodeToString(nonce[:])
	id, pending, err := c.startCall(ctx, "image.pull", request)
	if err != nil {
		return image.Record{}, err
	}
	type response struct {
		record image.Record
		err    error
	}
	done := make(chan response, 1)
	go func() {
		var record image.Record
		err := c.awaitCall(ctx, "image.pull", id, pending, &record)
		done <- response{record, err}
	}()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.Call(cleanup, "image.install", image.Install{Action: "abort", Token: request.Transfer}, nil)
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return image.Record{}, ctx.Err()
		case result := <-done:
			return result.record, result.err
		case <-ticker.C:
			var status image.Progress
			if err := c.Call(ctx, "image.status", struct {
				Token string `json:"token"`
			}{request.Transfer}, &status); err != nil {
				return image.Record{}, err
			}
			if status.Token != request.Transfer || status.Received < 0 || status.Total < 0 || status.Total > image.MaxCompressed || status.Received > status.Total {
				return image.Record{}, fmt.Errorf("the host returned invalid image progress")
			}
			runtimeLine(progress, "%s: %d/%d bytes\n", CleanLine(request.Ref), status.Received, status.Total)
		}
	}
}

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
)

func fetchImageHost(ctx context.Context, c *Conn, request image.Request, progress io.Writer) (record image.Record, returned error) {
	if err := requireImageTransferV1(c); err != nil {
		return record, err
	}
	correlation, err := imageTransferCorrelationV1()
	if err != nil {
		return record, err
	}
	request.Transfer = correlation
	var fetched image.Record
	done, err := startImageHostV1(ctx, c, request, &fetched)
	if err != nil {
		return record, err
	}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, stopImageTransferV1(ctx, c, correlation))
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return image.Record{}, ctx.Err()
		case err := <-done:
			return fetched, err
		case <-ticker.C:
			var status image.Progress
			if err := c.Call(ctx, "image.status", image.StatusRequest{Token: correlation}, &status); err != nil {
				return image.Record{}, err
			}
			if status.Token != correlation || status.Received < 0 || status.Total < 0 || status.Total > image.MaxCompressed || status.Received > status.Total {
				return image.Record{}, fmt.Errorf("the host returned invalid image progress")
			}
			runtimeLine(progress, "%s: %d/%d bytes\n", CleanLine(request.Ref), status.Received, status.Total)
		}
	}
}

// The actual frame is written before cancellation can send its abort. The
// same original pending result is retained until the native owner has joined.
func startImageHostV1(ctx context.Context, c *Conn, request image.Request, out *image.Record) (<-chan error, error) {
	done := make(chan error, 1)
	if intent, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1); intent != nil {
		call, err := intent.start(ctx, c, "image.pull", request, out)
		if err != nil {
			return nil, err
		}
		go func() { done <- call.await(context.WithoutCancel(ctx)) }()
	} else {
		id, pending, err := c.startCall(ctx, "image.pull", request)
		if err != nil {
			return nil, err
		}
		go func() { done <- c.awaitCall(context.WithoutCancel(ctx), "image.pull", id, pending, out) }()
	}
	return done, nil
}

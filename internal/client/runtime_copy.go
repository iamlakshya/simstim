package client

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

func copyRuntimeLaptop(ctx context.Context, c *Conn, request runtimecache.RuntimeRequest, status api.RuntimeStatus, w io.Writer, cacheDir string) (api.RuntimeStatus, error) {
	cache, err := runtimecache.Open(cacheDir)
	if err != nil {
		return status, err
	}
	defer cache.Close()
	present := map[string]bool{}
	for _, file := range status.Items {
		present[file.SHA256] = file.Present
	}
	for _, item := range request.Manifest.Items {
		if present[item.SHA256] {
			continue
		}
		started := time.Now()
		if err := cache.Fetch(ctx, item, runtimecache.HTTPClient(), nil); err != nil {
			return status, err
		}
		if err := copyRuntimeItem(ctx, c, request.Manifest, item, cache); err != nil {
			return status, err
		}
		runtimeLine(w, "%s: %d bytes in %s, sha256 ok\n", CleanLine(item.Name), item.Size, time.Since(started).Round(time.Millisecond))
		present[item.SHA256] = true
	}
	if err := c.Call(ctx, "runtime.install", runtimecache.RuntimeInstall{Action: "commit", Manifest: &request.Manifest, ImageDefaults: request.ImageDefaults}, nil); err != nil {
		return status, err
	}
	return runtimeReady(ctx, c, request)
}

func copyRuntimeItem(ctx context.Context, c *Conn, manifest supply.Manifest, item supply.Item, cache *runtimecache.Store) error {
	f, err := cache.OpenItem(ctx, item)
	if err != nil {
		return err
	}
	defer f.Close()
	var part runtimecache.RuntimePart
	if err := c.Call(ctx, "runtime.install", runtimecache.RuntimeInstall{Action: "begin", Manifest: &manifest, SHA256: item.SHA256}, &part); err != nil {
		return err
	}
	if part.Part == "" {
		return runtimeProtocolError()
	}
	defer abortRuntime(ctx, c, part.Part)
	buffer := make([]byte, runtimecache.ChunkSize)
	var offset int64
	for offset < item.Size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(f, buffer[:min(int64(len(buffer)), item.Size-offset)])
		if err != nil {
			return fmt.Errorf("read checked runtime cache: %w", err)
		}
		if err := c.Call(ctx, "runtime.install", runtimecache.RuntimeInstall{Action: "write", Part: part.Part, Offset: offset, Data: buffer[:n]}, nil); err != nil {
			return err
		}
		offset += int64(n)
	}
	return c.Call(ctx, "runtime.install", runtimecache.RuntimeInstall{Action: "finish", Part: part.Part}, nil)
}

func abortRuntime(ctx context.Context, c *Conn, token string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = c.Call(cleanup, "runtime.install", runtimecache.RuntimeInstall{Action: "abort", Part: token}, nil)
}

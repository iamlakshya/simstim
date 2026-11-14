package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

// HTTPClient makes bounded release requests. It sends no caller credentials.
// Release assets may redirect to a release CDN, always over HTTPS.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("runtime release redirect is unsafe")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	}}
}

// Fetch downloads one previously validated manifest item into its own part.
// The progress callback sees the owned part only while it is active.
func (s *Store) Fetch(ctx context.Context, item supply.Item, client *http.Client, progress func(*Part)) error {
	if has, err := s.Has(ctx, item); has || err != nil {
		return err
	}
	part, err := s.BeginContext(ctx, item)
	if err != nil {
		return err
	}
	defer part.Close()
	if progress != nil {
		progress(part)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return fmt.Errorf("create runtime release request: %w", err)
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return fmt.Errorf("runtime download needs HTTPS without credentials")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch runtime file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("runtime release returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > item.Size {
		return fmt.Errorf("runtime response exceeds its declared size")
	}
	buf := make([]byte, ChunkSize)
	var offset int64
	for offset < item.Size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := resp.Body.Read(buf[:min(int64(len(buf)), item.Size-offset)])
		if n > 0 {
			if err := part.WriteAt(buf[:n], offset); err != nil {
				return err
			}
			offset += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read runtime release: %w", readErr)
		}
		if n == 0 {
			return fmt.Errorf("runtime release made no progress")
		}
	}
	// Read at most one excess byte. It never reaches the partial file.
	var extra [1]byte
	n, err := io.ReadFull(resp.Body, extra[:])
	if n != 0 {
		return fmt.Errorf("runtime response exceeds its declared size")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("finish runtime release: %w", err)
	}
	return part.Finish(ctx)
}

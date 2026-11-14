package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

func decodeRuntime(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode runtime request: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("runtime request has trailing content")
	}
	return nil
}

func (b *bridge) handleRuntime(req proto.Request) bool {
	if req.Op != "runtime.fetch" && req.Op != "runtime.install" && req.Op != "runtime.status" {
		return false
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var result any
	var err error
	if req.Op == "runtime.install" {
		var a runtimecache.RuntimeInstall
		if err = decodeRuntime(req.Args, &a); err == nil {
			result, err = b.runtime.install(ctx, a)
		}
	} else {
		var a runtimecache.RuntimeRequest
		if err = decodeRuntime(req.Args, &a); err == nil {
			if req.Op == "runtime.fetch" {
				result, err = b.runtime.fetchRegistered(a, req.ID)
			} else {
				result, err = b.runtime.status(ctx, a)
			}
		}
	}
	b.reply(req.ID, result, runtimeFailure(err))
	return true
}

func (b *bridge) queueRuntimeFetch(req proto.Request) error {
	var a runtimecache.RuntimeRequest
	if err := decodeRuntime(req.Args, &a); err != nil {
		return err
	}
	return b.runtime.registerFetch(b.ctx, a, req.ID)
}

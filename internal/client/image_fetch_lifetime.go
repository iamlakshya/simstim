package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
)

type imageFetchAbortV1 struct {
	owner             *imageFetchIntentV1
	controller, token string
	done              chan struct{}
	err               error
}

func callImageOwned(ctx context.Context, c *Conn, operation string, args, out any) error {
	p, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1)
	if p == nil || operation == "image.status" {
		return c.Call(ctx, operation, args, out)
	}
	return p.call(ctx, c, operation, args, out)
}

func (p *imageFetchIntentV1) call(ctx context.Context, c *Conn, operation string, args, out any) error {
	call, err := p.start(ctx, c, operation, args, out)
	if err != nil {
		return err
	}
	return call.await(ctx)
}

func (call *imageFetchCallV1) await(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-call.done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return call.err
	}
}

func (p *imageFetchIntentV1) start(ctx context.Context, c *Conn, operation string, args, out any) (*imageFetchCallV1, error) {
	if p == nil || c != p.conn {
		return nil, imageFetchIntentError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wire, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode the original image fetch request: %w", err)
	}
	call := &imageFetchCallV1{op: operation, wire: wire, done: make(chan struct{})}
	if cleanup, _ := ctx.Value(imageFetchCleanupKeyV1{}).(*imageFetchAbortV1); cleanup != nil && cleanup.owner == p {
		if !p.turnMu.TryLock() {
			return nil, imageFetchIncompleteError()
		}
	} else {
		p.turnMu.Lock()
	}
	p.mu.Lock()
	if p.planned != nil {
		p.mu.Unlock()
		p.turnMu.Unlock()
		return nil, imageFetchIntentError()
	}
	p.planned = call
	p.mu.Unlock()
	id, pending, err := c.startCall(ctx, operation, args)
	p.mu.Lock()
	p.planned = nil
	if err != nil {
		if call.id != 0 {
			p.uncertain = true
		}
		call.err = err
		close(call.done)
	}
	p.mu.Unlock()
	p.turnMu.Unlock()
	if err != nil {
		return nil, err
	}
	if call.id != id {
		return nil, imageFetchIntentError()
	}
	// Cancellation asks the original host job to stop; it does not discard
	// this pending reply, which is the evidence that its handler actually ended.
	go func() {
		err := c.awaitCall(context.WithoutCancel(ctx), operation, id, pending, out)
		p.mu.Lock()
		call.err = err
		if !call.replied {
			p.uncertain = true
		}
		delete(p.turns, id)
		close(call.done)
		p.mu.Unlock()
	}()
	return call, nil
}

// The original correlation is the controller key. Once a valid begin ACK
// arrives, only its actual server token is selected for a new cleanup call.
// A pre-ACK cleanup already in flight keeps its originally selected alias.
func (p *imageFetchIntentV1) cleanupAliasLocked(token string) (string, string, bool) {
	if token == "" {
		return "", "", false
	}
	if p.installCorrelation != "" && (token == p.installCorrelation || token == p.installToken) {
		alias := p.installCorrelation
		if p.installToken != "" {
			alias = p.installToken
		}
		return p.installCorrelation, alias, true
	}
	if p.hostToken != "" && token == p.hostToken {
		return p.hostToken, p.hostToken, true
	}
	return "", "", false
}

func (p *imageFetchIntentV1) abort(ctx context.Context, token string) error {
	if p == nil || token == "" {
		return nil
	}
	p.mu.Lock()
	controller, alias, known := p.cleanupAliasLocked(token)
	if !known {
		p.mu.Unlock()
		return imageFetchIntentError()
	}
	if p.closedTokens[controller] {
		p.mu.Unlock()
		return nil
	}
	if previous := p.aborts[controller]; previous != nil {
		p.mu.Unlock()
		select {
		case <-previous.done:
			return previous.err
		case <-ctx.Done():
			return imageFetchIncompleteError()
		}
	}
	current := &imageFetchAbortV1{owner: p, controller: controller, token: alias, done: make(chan struct{})}
	p.aborts[controller] = current
	p.mu.Unlock()
	owned := context.WithValue(ctx, imageFetchPublicationKeyV1{}, p)
	owned = context.WithValue(owned, imageFetchCleanupKeyV1{}, current)
	call, err := p.start(owned, p.conn, "image.install", image.Install{Action: "abort", Token: alias}, nil)
	if err != nil {
		p.finishAbort(current, err)
		return err
	}
	// A caller deadline does not finish the original cleanup RPC. Retain its
	// controller record so a later close joins this same pending acknowledgement.
	go func() { p.finishAbort(current, call.await(context.WithoutCancel(owned))) }()
	select {
	case <-current.done:
		return current.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *imageFetchIntentV1) finishAbort(current *imageFetchAbortV1, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current.err = err
	if err != nil && p.aborts[current.controller] == current {
		delete(p.aborts, current.controller)
	}
	close(current.done)
}

func (p *imageFetchIntentV1) join(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	calls := append([]*imageFetchCallV1(nil), p.ops...)
	for _, call := range p.turns {
		if call.action == "chunk" {
			calls = append(calls, call)
		}
	}
	p.mu.Unlock()
	for _, call := range calls {
		select {
		case <-call.done:
		case <-ctx.Done():
			return imageFetchIncompleteError()
		}
		p.mu.Lock()
		replied := call.replied
		p.mu.Unlock()
		if !replied {
			return imageFetchIncompleteError()
		}
	}
	p.mu.Lock()
	uncertain := p.uncertain
	p.mu.Unlock()
	if uncertain {
		return imageFetchIncompleteError()
	}
	return nil
}

func (p *imageFetchIntentV1) stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var failures []error
	// The original begin correlation exists before its ACK. Its native owner
	// can cancel and join the same transfer even when that reply is lost.
	// abort selects the actual server alias after a valid original begin ACK.
	for pass := 0; pass < 2; pass++ {
		p.mu.Lock()
		host, install := p.hostToken, p.installCorrelation
		p.mu.Unlock()
		for _, token := range []string{host, install} {
			if token != "" {
				if err := p.abort(ctx, token); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if err := p.join(ctx); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func (p *imageFetchIntentV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closing = true
	finished, busy := p.finished, p.busy
	p.mu.Unlock()
	p.cancel()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 5*time.Second)
	defer cancel()
	if err := p.stop(ctx); err != nil {
		return err
	}
	if busy {
		select {
		case <-finished:
		case <-ctx.Done():
			return imageFetchIncompleteError()
		}
	}
	// Busy work may have finished its own cancellation after the first snapshot.
	// Recheck the original calls before releasing its retained source.
	if err := p.stop(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	p.closed = true
	p.resolved.registry = nil
	p.resolved.documents = nil
	p.resolved.layers = nil
	p.mu.Unlock()
	return nil
}

func imageFetchIncompleteError() *UserError {
	return &UserError{Msg: "the original image fetch has not confirmed its cancellation and cleanup", Hint: "Keep the original connection open and retry closing this request after its original operation ends.", Code: "image_fetch_incomplete", Kind: KindFailed}
}

// imageTransferCorrelationV1 names only a connection-owned controller. The
// host still creates its own storage token and never interprets this as a path.
func imageTransferCorrelationV1() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create the image transfer correlation: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}

func stopImageTransferV1(ctx context.Context, c *Conn, correlation string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	p, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1)
	if p == nil {
		return c.Call(cleanup, "image.install", image.Install{Action: "abort", Token: correlation}, nil)
	}
	if err := p.abort(cleanup, correlation); err != nil {
		return err
	}
	return p.join(cleanup)
}

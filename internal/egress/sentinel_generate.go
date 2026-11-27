package egress

import (
	"context"
	"crypto/rand"
	"io"
	"math/bits"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	maxSentinelAttempts    = 64
	maxSentinelChoiceDraws = 128
	sentinelEntropyBatch   = 4 << 10
)

// sentinelRead fills the entire batch or returns an error. A controlled
// source can honour cancellation during a read. No helper goroutine is
// spawned to pretend an arbitrary synchronous Reader can be interrupted.
type sentinelRead func(context.Context, []byte) error

type sentinelEntropy struct {
	read     sentinelRead
	buffer   [sentinelEntropyBatch]byte
	position int
}

func (e *sentinelEntropy) byte(ctx context.Context) (byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if e.position == len(e.buffer) {
		if err := e.read(ctx, e.buffer[:]); err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		e.position = 0
	}
	result := e.buffer[e.position]
	e.position++
	return result, nil
}

// Masked rejection is uniform. Its draw cap bounds even a faulty test or
// operating-system source without introducing a secret-length threshold.
func (e *sentinelEntropy) index(ctx context.Context, choices int) (int, bool) {
	if choices < 1 || choices > proto.MaxFrame {
		return 0, false
	}
	if choices == 1 {
		return 0, ctx.Err() == nil
	}
	width := bits.Len(uint(choices - 1)) //nolint:gosec // G115: choices is positive and bounded by proto.MaxFrame
	bytes := (width + 7) / 8
	mask := (uint64(1) << width) - 1
	for range maxSentinelChoiceDraws {
		var sample uint64
		for range bytes {
			b, err := e.byte(ctx)
			if err != nil {
				return 0, false
			}
			sample = sample<<8 | uint64(b)
		}
		sample &= mask
		if sample < uint64(choices) {
			return int(sample), true //nolint:gosec // G115: sample is below the positive bounded choice count
		}
	}
	return 0, false
}

// makeSentinel uses only operating-system cryptographic randomness. real nil
// permits an unarmed fresh start from safe persisted shape. The caller owns
// one prospective registry and atomically reserves a returned available value,
// excluding all current/prior values and sentinels, including the previous start.
func makeSentinel(ctx context.Context, name string, real *api.Secret, shape sentinelShape, available func(api.Secret) bool) (api.Secret, error) {
	read := func(_ context.Context, p []byte) error { _, err := io.ReadFull(rand.Reader, p); return err }
	return makeSentinelWithRead(ctx, name, real, shape, available, read)
}

func makeSentinelWithRead(ctx context.Context, name string, real *api.Secret, shape sentinelShape, available func(api.Secret) bool, read sentinelRead) (api.Secret, error) {
	plan, err := compileSentinelShape(ctx, name, shape)
	if err != nil {
		return "", err
	}
	if available == nil || read == nil {
		return "", sentinelFailure(name, "registry availability or randomness source is missing")
	}
	if real != nil {
		if err := plan.matches(ctx, name, *real); err != nil {
			return "", err
		}
	}
	if !plan.variable {
		return "", sentinelFailure(name, "configured token shape has no alternative sentinel")
	}
	entropy := &sentinelEntropy{read: read, position: sentinelEntropyBatch}
	candidate := make([]byte, 0, plan.bytes)
	for range maxSentinelAttempts {
		if err := sentinelContext(ctx, name); err != nil {
			return "", err
		}
		candidate = append(candidate[:0], plan.prefix...)
		for _, group := range plan.groups {
			for range group.count {
				if err := sentinelContext(ctx, name); err != nil {
					return "", err
				}
				index, ok := entropy.index(ctx, len(group.alphabet))
				if !ok {
					if err := sentinelContext(ctx, name); err != nil {
						return "", err
					}
					return "", sentinelFailure(name, "cryptographic randomness could not produce a bounded token choice")
				}
				candidate = utf8.AppendRune(candidate, group.alphabet[index])
			}
		}
		value := api.Secret(string(candidate))
		if real != nil && value == *real {
			continue
		}
		if available(value) {
			if err := sentinelContext(ctx, name); err != nil {
				return "", err
			}
			return value, nil
		}
	}
	if err := sentinelContext(ctx, name); err != nil {
		return "", err
	}
	return "", sentinelFailure(name, "unique sentinel attempt budget was exhausted")
}

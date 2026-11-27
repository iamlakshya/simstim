package api

import (
	"context"
	"io"
	"reflect"
)

// MatchesSource checks exact owner-supplied source and cancellation identity.
// It grants nothing: the actual inspection owns the ReaderAt/context/epoch.
// Byte adapters and non-comparable caller identities cannot match an owned
// FD-backed inspection. No bytes, source hashes or ambient readers are used.
func (p *JSONProjection) MatchesSource(ctx context.Context, reader io.ReaderAt, size int64) bool {
	if p == nil || p.raw != nil || ctx == nil || reader == nil || p.ctx == nil || p.r == nil || p.size != size || size < 0 {
		return false
	}
	if !reflect.ValueOf(ctx).Comparable() || !reflect.ValueOf(p.ctx).Comparable() || !reflect.ValueOf(reader).Comparable() || !reflect.ValueOf(p.r).Comparable() {
		return false
	}
	return ctx == p.ctx && reader == p.r
}

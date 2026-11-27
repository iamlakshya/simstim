//go:build !linux

package nativememory

import "context"

// CaptureCurrent refuses unsupported platforms rather than returning a
// zero-valued or unlimited provider for admission.
func CaptureCurrent(ctx context.Context) (*Current, error) {
	if ctx == nil {
		return nil, ErrUnknown
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}

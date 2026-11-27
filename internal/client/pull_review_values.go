package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// The original public getter runs first. Only a positively known legacy None
// empty set can continue without a receipt; modern missing metadata refuses.
// Both closures bind the SAME
// selected Conn/host/session, and neither receives names from a remote row.
func combinedPullReviewValues(ctx context.Context, public func(context.Context) ([]api.Secret, error), literal func(context.Context) ([][]byte, error)) ([][]byte, error) {
	standIns, err := public(ctx)
	if err != nil {
		return nil, err
	}
	values, err := literal(ctx)
	if err != nil {
		for _, value := range values {
			clear(value)
		}
		return nil, err
	}
	for _, standIn := range standIns {
		values = append(values, []byte(string(standIn)))
	}
	return values, nil
}

package host

import (
	"context"
	"errors"
)

// Admission can be cancelled by the original request. The one connection's
// actual publication reservation then outlives that request and every copied
// part. Cancelling one pull must not revoke another transfer's reservation.
func beginImagePublicationV1(attempt, connection context.Context) (*runtimePublication, error) {
	if attempt == nil || connection == nil {
		return nil, placementError("the image connection has no original owner context")
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if err := attempt.Err(); err != nil {
		return nil, err
	}
	if err := connection.Err(); err != nil {
		return nil, err
	}
	operation, err := beginPlacementPublicationV1(attempt, "publish")
	if err != nil {
		return nil, err
	}
	if err := connection.Err(); err != nil {
		return nil, errors.Join(err, operation.Close())
	}
	owned, cancel := context.WithCancelCause(connection)
	publication := &runtimePublication{ctx: context.WithValue(owned, publicationSourceContextV1{}, operation), cancel: cancel, owner: operation.owner, operation: operation, stop: make(chan struct{}), watched: make(chan struct{})}
	go publication.watch()
	return publication, nil
}

//go:build linux

package host

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// No exported constructor accepts a transaction, placement or session Source.
// This raw process creates all of them itself, once, and retains the original
// vector across each physical connection and every failed cleanup attempt.
type purgeAttemptServerV3 struct {
	image       *purgeAttemptImageV2
	endpoint    *purgeOwnedListenerV3
	domain      *purgeAttemptDomainV3
	code        *purgeCodeCustodyV3
	transaction *purgeActiveTransactionV2
	args        api.PurgeArgs
}

// RunPurgeAttemptProcessV3 is a raw dispatcher boundary. The caller must exit
// immediately with this result; no normal helper initialisation may precede
// the actual protected image producer.
func RunPurgeAttemptProcessV3() int {
	image, err := adoptRawPurgeAttemptImageV2()
	if err != nil {
		return 1
	}
	owner := &purgeAttemptServerV3{image: image}
	owner.endpoint, err = newPurgeOwnedListenerV3(image.lifetime, image)
	if err != nil {
		// No request or session capture has occurred at this boundary.
		_ = image.Close()
		return 1
	}
	originalRequest, joinOriginal, err := image.watchOriginalRequestV2(image.lifetime)
	if err != nil {
		_ = owner.endpoint.Close()
		_ = image.Close()
		return 1
	}
	defer joinOriginal()
	for {
		if owner.code == nil && originalRequest.Err() != nil {
			_ = owner.endpoint.Close()
			_ = image.Close()
			return 1
		}
		if owner.code != nil && owner.endpoint.closing && !owner.endpoint.closed {
			if err := owner.endpoint.Close(); err != nil {
				time.Sleep(25 * time.Millisecond)
				continue
			}
		}
		if owner.code != nil && owner.endpoint.closed {
			if err := owner.code.CloseAfterTransaction(owner.transaction); err == nil {
				if err := owner.domain.Close(); err == nil {
					_ = owner.image.Close()
					return 0
				}
			}
			endpoint, err := createPurgeOwnedListenerV3(owner.image.lifetime, owner.image, owner.code)
			if err != nil {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			owner.endpoint, owner.code.endpoint = endpoint, endpoint
		}
		if owner.code == nil {
			_ = owner.endpoint.listener.SetDeadline(time.Now().Add(100 * time.Millisecond))
		} else {
			_ = owner.endpoint.listener.SetDeadline(time.Time{})
		}
		connection, err := owner.endpoint.listener.AcceptUnix()
		if err != nil {
			if temporary, ok := err.(net.Error); ok && temporary.Timeout() {
				continue
			}
			if owner.code == nil {
				_ = owner.endpoint.Close()
				_ = image.Close()
				return 1
			}
			// An endpoint error supplies no release fact. Preserve this original
			// owner and retry its original native listener rather than exit.
			time.Sleep(25 * time.Millisecond)
			continue
		}
		finished := owner.serveConnectionV3(connection)
		if finished {
			return 0
		}
	}
}

func (owner *purgeAttemptServerV3) serveConnectionV3(connection *net.UnixConn) bool {
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		_ = connection.Close()
		return false
	}
	defer line.Close()
	bootstrap, cancel := context.WithTimeout(owner.image.lifetime, 10*time.Second)
	defer cancel()
	_ = line.SetDeadline(time.Now().Add(10 * time.Second))
	if err := owner.endpoint.Check(); err != nil {
		return false
	}
	if err := line.writePurgeAttemptNativeHelloV2(bootstrap, owner.image); err != nil {
		return false
	}
	peer, err := acceptPurgeOperatorHelloV3(bootstrap, line, owner.image, owner.code == nil)
	if err != nil {
		return false
	}
	first := true
	for {
		request, data, err := readPurgeRequestV3(bootstrap, line, peer)
		if err != nil {
			return owner.restoreDisconnectedV3()
		}
		if owner.code == nil {
			if request.Op != purgeBeginV3 {
				_ = writePurgeResponseV3(line, request.ID, api.PurgeResult{}, placementError("a new actual purge owner accepts only its original begin"))
				return false
			}
			sender, err := line.currentSender()
			if err != nil || owner.image.checkOriginalOperatorParentV3(bootstrap, peer, sender) != nil {
				return false
			}
			domain, err := newIndependentPurgeAttemptDomainV3(bootstrap, owner.image, data.Args)
			if err == nil {
				err = domain.Install(bootstrap)
			}
			if err != nil {
				if domain != nil {
					_ = domain.Close()
				}
				_ = writePurgeResponseV3(line, request.ID, api.PurgeResult{}, err)
				return false
			}
			owner.domain, owner.args = domain, data.Args
			code, err := receivePurgeCodeCustodyV3(bootstrap, owner.image, line, peer, request, data)
			if err != nil {
				// The actual parent still retains its factory until this native
				// process exits. No original session has been captured here.
				if code != nil {
					_ = code.closeBorrowedV3()
				}
				_ = owner.domain.Close()
				owner.domain = nil
				_ = writePurgeResponseV3(line, request.ID, api.PurgeResult{}, err)
				return false
			}
			owner.code = code
			owner.code.endpoint = owner.endpoint
			owner.image.mu.Lock()
			owner.image.custody = code
			owner.image.mu.Unlock()
			owner.transaction, err = newOwnedPurgeTransactionV2(owner.image.lifetime, owner.args)
			if err != nil {
				_ = writePurgeResponseV3(line, request.ID, api.PurgeResult{}, err)
				return false
			}
			owner.code.transaction = owner.transaction
		} else if data.Args != owner.args || request.Op == purgeBeginV3 {
			_ = writePurgeResponseV3(line, request.ID, api.PurgeResult{}, placementError("this original purge owner accepts only its exact saved continuation"))
			return false
		}
		if err := line.RejectAncillary(); err != nil {
			return false
		}
		_ = line.SetDeadline(time.Time{})
		requestContext, join, err := purgeRequestLifetimeV3(owner.image.lifetime, line, peer)
		if err != nil {
			return owner.restoreDisconnectedV3()
		}
		if first && request.Op == purgeBeginV3 {
			original, originalJoin, watchErr := owner.image.watchOriginalRequestV2(requestContext)
			if watchErr != nil {
				join()
				return owner.restoreDisconnectedV3()
			}
			priorJoin := join
			requestContext, join = original, func() { originalJoin(); priorJoin() }
		}
		first = false
		result, failure, finish := owner.dispatchV3(requestContext, request.Op)
		join()
		writeErr := writePurgeResponseV3(line, request.ID, result, failure)
		if finish {
			if writeErr == nil {
				owner.waitFinalResponseAckV3(line, peer, request.ID)
			}
			_ = owner.image.Close()
			return true
		}
		if writeErr != nil {
			return owner.restoreDisconnectedV3()
		}
		// Bound idle transport only. This deadline cannot end owner custody.
		bootstrap = owner.image.lifetime
		_ = line.SetDeadline(time.Now().Add(30 * time.Second))
	}
}

// Physical transaction/code/domain cleanup has ALREADY completed. This short
// same-line acknowledgement only keeps the final sender live for its DATA
// receiver. No acknowledgement, timeout or EOF authorises an unlink or Source.
func (owner *purgeAttemptServerV3) waitFinalResponseAckV3(line *StartupLineV1, peer *purgeRequestPeerV3, id uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = line.SetDeadline(time.Now().Add(5 * time.Second))
	frame, err := line.ReadFrame()
	if err != nil || line.RejectAncillary() != nil {
		return
	}
	sender, err := line.currentSender()
	if err != nil || peer.CheckSender(ctx, sender) != nil {
		return
	}
	var acknowledgement proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, acknowledgement) || json.Unmarshal(frame.Payload, &acknowledgement) != nil || acknowledgement.ID != id || !acknowledgement.OK || acknowledgement.Error != nil || len(acknowledgement.Result) != 0 {
		return
	}
}

func (owner *purgeAttemptServerV3) dispatchV3(request context.Context, operation string) (api.PurgeResult, error, bool) {
	if owner.transaction == nil {
		return api.PurgeResult{}, placementError("the original purge transaction is not available"), false
	}
	transaction := owner.transaction
	switch operation {
	case purgeBeginV3, purgeContinueV3:
		result, err := transaction.Continue(request)
		return result, err, false
	case purgeAbortV3:
		err := transaction.AbortBeforeStop(request)
		return transaction.reportV2(), err, false
	case purgeCloseV3:
		if err := transaction.CloseAfterCleanup(request); err != nil {
			return transaction.reportV2(), err, false
		}
		if err := owner.code.CloseAfterTransaction(transaction); err != nil {
			return transaction.reportV2(), err, false
		}
		if err := owner.domain.Close(); err != nil {
			return transaction.reportV2(), err, false
		}
		// The response uses only saved public DATA after all native subjects
		// close. Image lifetime closes after the last physical response.
		return transaction.reportV2(), nil, true
	default:
		return transaction.reportV2(), placementError("the original purge owner refuses an unknown continuation"), false
	}
}

func (owner *purgeAttemptServerV3) restoreDisconnectedV3() bool {
	if owner.transaction == nil {
		return false
	}
	// Abort's actual producer checks refuse after ANY irreversible dispatch.
	// Only a positively restored and actually closed original vector can
	// retire this temporary owner after its client disappears.
	if err := owner.transaction.AbortBeforeStop(context.Background()); err != nil {
		return false
	}
	if err := owner.transaction.CloseAfterCleanup(context.Background()); err != nil {
		return false
	}
	if err := owner.code.CloseAfterTransaction(owner.transaction); err != nil {
		return false
	}
	if err := owner.domain.Close(); err != nil {
		return false
	}
	_ = owner.image.Close()
	return true
}

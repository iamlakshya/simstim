package client

import (
	"context"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

// NewImageSaveGrantRequest retains one image-save operation through its
// questions. It supplies no local value-read or remote value-send permission.
// Bind it with WithGrantRequest before opening the original host connection.
func NewImageSaveGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantImageSave, operation: grantOpImageSave}
}

// This guard is created only from the original accepted saved connection.
// The public facade owns its typed request/review/keep/drop state machine;
// no DTO, returned host ID or renewed connection can construct this guard.
type imageSaveOperation struct {
	mu                       sync.Mutex
	request                  *GrantRequest
	conn                     *Conn
	stamp                    *hostTransportStamp
	id, ref                  string
	replace, untrusted       bool
	creation                 *SandboxPolicy
	receipt                  *sandboxDeliveryReceipt
	settings                 *settingsOperation
	view                     SecretConfigView
	names, sent, missing     []string
	values                   map[string]api.Secret
	fetched, attempted       bool
	busy, publishing, closed bool
	review                   *image.SaveReview
	reviewProgress           *imageSaveReviewProgress
	pendingID                string
	reviewFailure            error
	facade                   any
}

func newImageSaveOperation(ctx context.Context, c *Conn, id, ref string, replace bool) (*imageSaveOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return nil, err
	}
	if c.hostTransport.kind != hostTransportSaved || c.agentOnly || !ValidHostID(c.HostID()) || c.HostIDChanged() || !image.ValidSaveRef(ref) {
		return nil, grantOperationError()
	}
	if err := checkSessionID(c.Host.Name, id); err != nil {
		return nil, err
	}
	if !c.HasFeature(proto.FeatureImageSave) {
		return nil, c.grantHelperOld("save a microvm image")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return nil, err
	}
	if r.purpose != grantImageSave || r.operation != grantOpImageSave || r.SSHAgent || r.newSecrets != nil || r.freshChild != nil || r.newInputs != nil || r.wall != nil {
		return nil, grantOperationError()
	}
	if before := r.imageSave; before != nil {
		if before.request != r || before.conn != c || before.stamp != c.hostTransport || before.id != id || before.ref != ref || before.replace != replace || before.untrusted != r.Untrusted {
			return nil, grantOperationError()
		}
		return before, nil
	}
	op := &imageSaveOperation{request: r, conn: c, stamp: c.hostTransport, id: id, ref: ref, replace: replace, untrusted: r.Untrusted}
	r.operationUntrusted = r.Untrusted
	r.imageSave = op
	return op, nil
}

func (o *imageSaveOperation) checkRequest(ctx context.Context, cleanup bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o == nil || o.request == nil || o.conn == nil || o.stamp == nil || grantRequestOf(ctx) != o.request {
		return grantOperationError()
	}
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if closed {
		return grantOperationError()
	}
	r := o.request
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.imageSave != o || r.purpose != grantImageSave || r.operation != grantOpImageSave || r.Untrusted != o.untrusted || r.SSHAgent {
		return grantOperationError()
	}
	if !cleanup {
		return r.lifetimeErrorLocked()
	}
	// Cancellation admits only the facade's fixed original drop cleanup.
	// It cannot renew a closed request, authorise keep or read/send values.
	if r.ending || r.preparationClosed || r.requestContext == nil {
		return grantOperationError()
	}
	return nil
}

func (o *imageSaveOperation) checkSource(ctx context.Context) error {
	if o == nil || o.conn == nil || o.stamp == nil || o.conn.hostTransport != o.stamp {
		return grantOperationError()
	}
	if err := o.stamp.checkConn(o.conn); err != nil {
		return err
	}
	current, err := captureHostSelectionSource(ctx, o.conn.Host)
	if err != nil {
		return err
	}
	if !sameHostTransportSource(o.stamp.source, current) {
		return grantOperationError()
	}
	return nil
}

func (o *imageSaveOperation) check(ctx context.Context) error {
	if err := o.checkRequest(ctx, false); err != nil {
		return err
	}
	return o.checkSource(ctx)
}

// begin uses the existing exact Source/OP publication path. The caller must
// release it after the actual frame write, before waiting for a response or
// asking a question. A local pin write may retain it only for that write.
func (o *imageSaveOperation) begin(ctx context.Context, kind string) (context.Context, func(), error) {
	if err := o.check(ctx); err != nil {
		return ctx, nil, err
	}
	owned, release, err := o.conn.beginGrantSource(ctx, kind, true)
	if err != nil {
		return ctx, nil, err
	}
	if err := o.check(owned); err != nil {
		release()
		return ctx, nil, err
	}
	if o.creation != nil {
		if _, err := o.checkValuesLocal(owned); err != nil {
			release()
			return ctx, nil, err
		}
	}
	return owned, release, nil
}

func (o *imageSaveOperation) beginCleanup(ctx context.Context) (context.Context, func(), error) {
	if err := o.checkRequest(ctx, true); err != nil {
		return ctx, nil, err
	}
	if err := o.checkSource(ctx); err != nil {
		return ctx, nil, err
	}
	// beginGrant normally checks the original cancelled request lifetime.
	// Cleanup does not mint or bind another R: it checks the same exact
	// saved Source/physical OP and then sends only the retained drop ID.
	owned, release, err := o.conn.beginGrantSource(context.WithValue(ctx, grantRequestKey{}, (*GrantRequest)(nil)), "drop image save", true)
	if err != nil {
		return ctx, nil, err
	}
	owned = context.WithValue(owned, grantRequestKey{}, o.request)
	if err := o.checkRequest(owned, true); err != nil {
		release()
		return ctx, nil, err
	}
	if err := o.checkSource(owned); err != nil {
		release()
		return ctx, nil, err
	}
	if err := o.checkCleanupSelection(owned); err != nil {
		release()
		return ctx, nil, err
	}
	return owned, release, nil
}

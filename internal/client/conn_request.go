package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// requestStage observes the same queued/upload publication stages in tests.
var requestStage = func(string) {}

// Call uses the ONE ordered request writer and pending-response table.
func (c *Conn) Call(ctx context.Context, op string, args any, out any) error {
	return c.callWithSent(ctx, op, args, out, nil)
}

// callWithSent releases publication immediately after the actual frame write.
func (c *Conn) callWithSent(ctx context.Context, op string, args any, out any, afterWrite func()) error {
	var once sync.Once
	release := func() {
		once.Do(func() {
			if afterWrite != nil {
				afterWrite()
			}
		})
	}
	defer release()
	id, ch, err := c.startCall(ctx, op, args)
	if err != nil {
		return err
	}
	release()
	return c.awaitCall(ctx, op, id, ch, out)
}

// startCall returns only AFTER the same physical ordered frame writer. Runtime
// aborts can therefore follow their fetch without racing request publication.
// New's private wire/ACK witness shares this path; there is no parallel RPC.
func (c *Conn) startCall(ctx context.Context, op string, args any) (uint64, chan proto.Response, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if err := checkClientImagePolicyWireV1(op, args); err != nil {
		return 0, nil, err
	}
	id := c.nextID.Add(1)
	req := proto.Request{ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return 0, nil, fmt.Errorf("encode the %s request: %w", op, err)
		}
		req.Args = b
	}
	if err := prepareHostAttemptNewRequestV1(ctx, c, &req); err != nil {
		return 0, nil, err
	}
	imageFetch := originalImageFetchV1(ctx)
	upgrade, _ := ctx.Value(runtimeUpgradePublicationKeyV1{}).(*runtimeUpgradeIntentV1)
	witness, _ := ctx.Value(newPublicationKey{}).(*newPublicationWitness)
	fresh, _ := ctx.Value(freshChildPublicationKey{}).(*freshChildPublicationWitness)
	var releaseUpgrade func()
	defer func() {
		if releaseUpgrade != nil {
			releaseUpgrade()
		}
	}()
	beforeFrame := func() error {
		c.mu.Lock()
		ended := c.err
		c.mu.Unlock()
		if ended != nil {
			return ended
		}
		if err := revalidateOriginalStartSettingsV1(ctx, op); err != nil {
			return err
		}
		if err := beforeRewindRequestV1(ctx, c, req, args); err != nil {
			return err
		}
		if req.Op == "image.pull" || req.Op == "image.install" || req.Op == "image.status" {
			if err := imageFetch.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if req.Op == "sandbox.prepare" {
			if err := sandboxPrepareBeforeRequestV1(ctx, c, req, args); err != nil {
				return err
			}
		}
		if err := checkAutoNewWireV1(ctx, c, req, args); err != nil {
			return err
		}
		if req.Op == "new" {
			if err := checkHostSelectionPublicationV1(ctx, c); err != nil {
				return err
			}
		}
		if err := beforeTrustedJudgeRequestV1(ctx, c, req, args); err != nil {
			return err
		}
		if err := beforeNamedSSHKeysRequestV1(ctx, c, req, args); err != nil {
			return err
		}
		if err := purgeBeforeRequest(ctx, c, req, args); err != nil {
			return err
		}
		if err := networkHoldBeforeRequestV1(ctx, c, req, args); err != nil {
			return err
		}
		if err := policyReplaceBeforeRequest(ctx, c, req, args); err != nil {
			return err
		}
		if upgrade != nil {
			owned, release, err := upgrade.beforeRequest(ctx, c, req, args)
			if err != nil {
				return err
			}
			releaseUpgrade = release
			ctx = owned
		}
		if err := beforeHostAttemptNewRequestV1(ctx, c, req, args); err != nil {
			return err
		}
		if witness != nil {
			if err := witness.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if push, _ := ctx.Value(pushPublicationKey{}).(*pushPublicationWitness); push != nil {
			if err := push.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if fresh != nil {
			if err := fresh.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if imageSave, _ := ctx.Value(imageSavePublicationKey{}).(*imageSaveOperation); imageSave != nil {
			if err := imageSave.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if imageResume, _ := ctx.Value(resumeImagePublicationKeyV1{}).(*resumeImagePolicyV1); imageResume != nil {
			if err := imageResume.beforeRequest(ctx, c, req, args); err != nil {
				return err
			}
		}
		if plan, _ := ctx.Value(newPlanPublicationKey{}).(*newSecretPlan); plan != nil {
			if err := plan.markPublicationAttempt(); err != nil {
				return err
			}
		}
		if fresh != nil {
			if err := fresh.plan.markPublicationAttempt(); err != nil {
				return err
			}
		}
		if err := markRewindPublicationV1(ctx, c, req); err != nil {
			return err
		}

		return nil
	}
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return 0, nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	requestStage("queued")
	var gateErr error
	if err := c.w.WriteJSONGuardedCtx(ctx, proto.TRequest, req, func() error { gateErr = beforeFrame(); return gateErr }); err != nil {
		c.drop(id)
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if gateErr != nil {
			return 0, nil, gateErr
		}
		return 0, nil, ErrClosed
	}
	if imageFetch != nil && (op == "image.pull" || op == "image.install" || op == "image.status") {
		imageFetch.wroteRequest(c, id)
	}
	if named := originalNamedSSHPolicyV1(ctx); named != nil && op == "ssh-key.policy" {
		named.wroteRequest(c, id)
	}
	if op == "sandbox.prepare" {
		sandboxPrepareWroteRequestV1(ctx, c, id)
	}
	wroteHostAttemptNewRequestV1(ctx, c, id)
	if witness != nil {
		witness.wroteRequest(c, id)
	}
	if fresh != nil {
		fresh.wroteRequest(c, id)
	}
	if rewind := originalRewindV1(ctx); rewind != nil && op == api.OpRewindCommitV1 {
		rewind.mu.Lock()
		witness := rewind.publication
		rewind.mu.Unlock()
		if witness != nil {
			witness.wroteRequestV3(c, id)
		}
	}
	if upgrade != nil {
		upgrade.wroteRequest(c, id)
	}
	return id, ch, nil
}

func (c *Conn) awaitCall(ctx context.Context, op string, id uint64, ch chan proto.Response, out any) error {
	return c.awaitCallResult(ctx, op, id, ch, out, false)
}

// Only explicit typed purge/report callers request partial result decoding.
// A refusal never becomes a successful publication or an ACK receipt.
func (c *Conn) awaitCallResult(ctx context.Context, op string, id uint64, ch chan proto.Response, out any, partial bool) error {
	select {
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if resp.ID != id {
			return &UserError{Msg: "the host answered a different request", Hint: "Reconnect to the host and retry the operation.", Code: "protocol", Kind: KindRefused}
		}
		var imageFetchErr error
		if imageFetch := originalImageFetchV1(ctx); imageFetch != nil && (op == "image.pull" || op == "image.install" || op == "image.status") {
			imageFetchErr = imageFetch.afterResponse(ctx, c, op, id, resp, out)
		}
		if op == "sandbox.prepare" {
			imageFetchErr = errors.Join(imageFetchErr, sandboxPrepareAfterResponseV1(ctx, c, op, id, resp))
		}
		var namedErr error
		if named := originalNamedSSHPolicyV1(ctx); named != nil && op == "ssh-key.policy" {
			namedErr = named.afterResponse(ctx, c, op, id, resp)
		}
		if (resp.OK || partial) && out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return errors.Join(fmt.Errorf("read the answer to %s: %w", op, err), namedErr, imageFetchErr)
			}
		}
		if !resp.OK {
			if op == "new" && resp.Error != nil && (resp.Error.Code == "no_room" || hasNewNoRoomControlV1(resp.Error.Data)) {
				return receiveHostAttemptNewRefusalV1(ctx, c, resp)
			}
			if resp.Error != nil {
				return errors.Join(hostErrorOn(c.Host.Name, resp.Error), namedErr, imageFetchErr)
			}
			return errors.Join(errors.New("the host refused the request"), namedErr, imageFetchErr)
		}
		if namedErr != nil || imageFetchErr != nil {
			return errors.Join(namedErr, imageFetchErr)
		}
		if witness, _ := ctx.Value(newPublicationKey{}).(*newPublicationWitness); witness != nil {
			if err := witness.afterResponse(ctx, c, resp, out); err != nil {
				return err
			}
		}
		if fresh, _ := ctx.Value(freshChildPublicationKey{}).(*freshChildPublicationWitness); fresh != nil {
			if err := fresh.afterResponse(ctx, c, resp, out); err != nil {
				return err
			}
		}
		if rewind := originalRewindV1(ctx); rewind != nil && op == api.OpRewindCommitV1 {
			rewind.mu.Lock()
			witness := rewind.publication
			rewind.mu.Unlock()
			if witness == nil {
				return sandboxReceiptError()
			}
			if err := witness.afterResponseV3(ctx, c, resp, out); err != nil {
				return err
			}
		}
		if upgrade, _ := ctx.Value(runtimeUpgradePublicationKeyV1{}).(*runtimeUpgradeIntentV1); upgrade != nil {
			if err := upgrade.afterResponse(ctx, c, resp, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *Conn) drop(id uint64) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }

func originalImageFetchV1(ctx context.Context) *imageFetchIntentV1 {
	if selected, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1); selected != nil {
		return selected
	}
	if request := grantRequestOf(ctx); request != nil {
		request.mu.Lock()
		defer request.mu.Unlock()
		return request.imageFetch
	}
	return nil
}

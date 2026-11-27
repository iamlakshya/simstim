package client

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

type runtimeUpgradePublicationKeyV1 struct{}

type runtimeUpgradeFrameV1 struct {
	action, token, digest string
	next                  int64
	written               bool
}

type runtimeUpgradePartV1 struct {
	digest       string
	size, offset int64
}

// beforeRequest retains the existing native publication gate only through the
// actual ordered frame write. No lock survives into a response or question.
func (intent *runtimeUpgradeIntentV1) beforeRequest(ctx context.Context, c *Conn, req proto.Request, args any) (context.Context, func(), error) {
	noop := func() {}
	if intent == nil || c != intent.conn || c.hostTransport != intent.stamp || c.HostID() != intent.hostID {
		return ctx, noop, grantOperationError()
	}
	if a, ok := args.(runtimecache.RuntimeInstall); req.Op == "runtime.install" && ok && a.Action == "abort" {
		// Cancellation keeps only the exact original token on its original
		// connection. It cannot acquire another Source, value or start grant.
		intent.mu.Lock()
		_, part := intent.parts[a.Part]
		_, fetch := intent.fetches[a.Part]
		intent.mu.Unlock()
		if a.Manifest != nil || a.ImageDefaults != nil || a.SHA256 != "" || a.Offset != 0 || len(a.Data) != 0 || (!part && !fetch) {
			return ctx, noop, grantOperationError()
		}
		return ctx, noop, nil
	}
	if err := intent.check(ctx); err != nil {
		return ctx, noop, err
	}
	if req.Op == "runtime.status" {
		a, ok := args.(runtimecache.RuntimeRequest)
		if !ok {
			return ctx, noop, grantOperationError()
		}
		intent.mu.Lock()
		knownTransfer := a.Transfer == "" || intent.fetches[a.Transfer] != 0
		intent.mu.Unlock()
		a.Transfer = ""
		if !knownTransfer || !reflect.DeepEqual(a, intent.runtime) {
			return ctx, noop, grantOperationError()
		}
		return ctx, noop, nil
	}
	owned, release, err := c.beginGrantSource(ctx, "runtime upgrade", true)
	if err != nil {
		return ctx, noop, err
	}
	fail := func(err error) (context.Context, func(), error) { release(); return ctx, noop, err }
	if err := intent.check(owned); err != nil {
		return fail(err)
	}
	settings, err := intent.settings.Settings(owned)
	if err != nil {
		return fail(err)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return fail(err)
	}
	entry, found := tiers.Entry(c.Host.Name)
	if !found || entry.HostID != intent.hostID || entry.IDChanged {
		return fail(grantIdentityError(c.Host))
	}
	if err := allowRequestGrant(owned, c.Host, intent.hostID, tiers.Of(c.Host.Name), Outbound{Kind: OutboundSilentUpgrade}, settings); err != nil {
		return fail(err)
	}
	intent.mu.Lock()
	defer intent.mu.Unlock()
	frame := runtimeUpgradeFrameV1{}
	switch req.Op {
	case "runtime.fetch":
		a, ok := args.(runtimecache.RuntimeRequest)
		if !ok || intent.mode == "laptop" || !ValidHostID(a.Transfer) {
			return fail(grantOperationError())
		}
		token := a.Transfer
		a.Transfer = ""
		if !reflect.DeepEqual(a, intent.runtime) || intent.fetches[token] != 0 {
			return fail(grantOperationError())
		}
		frame.action, frame.token = "fetch", token
		intent.fetches[token] = req.ID
	case "runtime.install":
		a, ok := args.(runtimecache.RuntimeInstall)
		if !ok || intent.mode == "host" {
			return fail(grantOperationError())
		}
		frame.action, frame.token, frame.digest = a.Action, a.Part, a.SHA256
		switch a.Action {
		case "begin":
			if a.Manifest == nil || !reflect.DeepEqual(*a.Manifest, intent.runtime.Manifest) || a.ImageDefaults != nil || a.Part != "" || a.Offset != 0 || len(a.Data) != 0 {
				return fail(grantOperationError())
			}
			for _, item := range intent.runtime.Manifest.Items {
				if item.SHA256 == a.SHA256 {
					frame.next = item.Size
				}
			}
			if frame.next <= 0 {
				return fail(grantOperationError())
			}
		case "write", "finish":
			part := intent.parts[a.Part]
			if part == nil || a.Manifest != nil || a.ImageDefaults != nil || a.SHA256 != "" {
				return fail(grantOperationError())
			}
			if a.Action == "write" {
				if a.Offset != part.offset || len(a.Data) == 0 || len(a.Data) > runtimecache.ChunkSize || int64(len(a.Data)) > part.size-part.offset {
					return fail(grantOperationError())
				}
				frame.next = part.offset + int64(len(a.Data))
			} else if a.Offset != 0 || len(a.Data) != 0 || part.offset != part.size {
				return fail(grantOperationError())
			}
		case "commit":
			if a.Manifest == nil || !reflect.DeepEqual(*a.Manifest, intent.runtime.Manifest) || !reflect.DeepEqual(a.ImageDefaults, intent.runtime.ImageDefaults) || a.Part != "" || a.SHA256 != "" || a.Offset != 0 || len(a.Data) != 0 {
				return fail(grantOperationError())
			}
		default:
			return fail(grantOperationError())
		}
	default:
		return fail(grantPurposeError())
	}
	intent.frames[req.ID] = frame
	return owned, release, nil
}

func (intent *runtimeUpgradeIntentV1) wroteRequest(c *Conn, id uint64) {
	intent.mu.Lock()
	defer intent.mu.Unlock()
	if c != intent.conn {
		return
	}
	frame, found := intent.frames[id]
	if !found {
		return
	}
	frame.written = true
	intent.frames[id] = frame
	if frame.action == "write" {
		if part := intent.parts[frame.token]; part != nil {
			part.offset = frame.next
		}
	}
}

func (intent *runtimeUpgradeIntentV1) afterResponse(ctx context.Context, c *Conn, response proto.Response, out any) error {
	if c != intent.conn || c.hostTransport != intent.stamp {
		return grantOperationError()
	}
	intent.mu.Lock()
	defer intent.mu.Unlock()
	frame, found := intent.frames[response.ID]
	if !found {
		return nil
	} // Read-only status has no mutation receipt.
	if !frame.written {
		return grantOperationError()
	}
	delete(intent.frames, response.ID)
	if frame.action != "begin" {
		return nil
	}
	part, ok := out.(*runtimecache.RuntimePart)
	if !ok || !ValidHostID(part.Part) || intent.parts[part.Part] != nil || intent.fetches[part.Part] != 0 {
		return runtimeProtocolError()
	}
	intent.parts[part.Part] = &runtimeUpgradePartV1{digest: frame.digest, size: frame.next}
	return ctx.Err()
}

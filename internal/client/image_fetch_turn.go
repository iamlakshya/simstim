package client

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// A call entry belongs to the existing physical writer and original pending
// response, never an invented reply or a second response table.
type imageFetchCallV1 struct {
	id                                    uint64
	op, action, token, digest, controller string
	offset                                int64
	bytes                                 int
	wire                                  []byte
	wrote, replied                        bool
	done                                  chan struct{}
	err                                   error
}

func sameImageFetchPinV1(left, right image.Request) bool {
	return left.Ref == right.Ref && left.Digest == right.Digest && left.PlatformDigest == right.PlatformDigest && left.Arch == right.Arch && reflect.DeepEqual(left.SavedHost, right.SavedHost)
}

func (p *imageFetchIntentV1) beforeRequest(ctx context.Context, c *Conn, request proto.Request, _ any) error {
	if p == nil {
		if r := grantRequestOf(ctx); r != nil && (requestGrantOperation(r) == grantOpNew || requestGrantOperation(r) == grantOpSandboxPrepare) && (request.Op == "image.pull" || request.Op == "image.install") {
			return imageFetchIntentError()
		}
		return nil
	}
	if request.Op != "image.pull" && request.Op != "image.install" && request.Op != "image.status" {
		return nil
	}
	if request.Op == "image.status" {
		return p.beforeStatus(ctx, c, request)
	}
	var install image.Install
	if request.Op == "image.install" {
		if !api.ExactJSONFields(request.Args, install) || json.Unmarshal(request.Args, &install) != nil {
			return imageFetchIntentError()
		}
		if cleanup, _ := ctx.Value(imageFetchCleanupKeyV1{}).(*imageFetchAbortV1); install.Action == "abort" && cleanup != nil && cleanup.owner == p {
			return p.cleanupFrame(ctx, c, request, install)
		}
	}
	if err := p.confirmed(ctx, c); err != nil {
		return err
	}
	if current, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1); current != p {
		return imageFetchIntentError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := p.planned
	if entry == nil || entry.op != request.Op || !reflect.DeepEqual(entry.wire, []byte(request.Args)) || p.complete || p.closed || p.closing || p.uncertain || p.turns[request.ID] != nil {
		return imageFetchIntentError()
	}
	if request.Op == "image.pull" {
		if err := p.admitPull(request, entry); err != nil {
			return err
		}
	} else {
		if err := p.admitInstall(install, entry); err != nil {
			return err
		}
	}
	entry.id = request.ID
	p.turns[entry.id] = entry
	if entry.action != "chunk" {
		p.ops = append(p.ops, entry)
	}
	return nil
}

func (p *imageFetchIntentV1) beforeStatus(ctx context.Context, c *Conn, request proto.Request) error {
	var status image.StatusRequest
	if !api.ExactJSONFields(request.Args, status) || json.Unmarshal(request.Args, &status) != nil {
		return imageFetchIntentError()
	}
	if status.Request != nil && status.Token == "" {
		if !sameImageFetchPinV1(*status.Request, p.resolved.request) || status.Request.Transfer != "" {
			return imageFetchIntentError()
		}
		return p.check(ctx, c)
	}
	if err := p.confirmed(ctx, c); err != nil {
		return err
	}
	if current, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1); current != p {
		return imageFetchIntentError()
	}
	p.mu.Lock()
	known := status.Token != "" && (status.Token == p.hostToken || status.Token == p.installToken)
	p.mu.Unlock()
	if !known {
		return imageFetchIntentError()
	}
	return nil
}

// Admission runs while the intent owns its physical-writer metadata lock.
func (p *imageFetchIntentV1) admitPull(request proto.Request, entry *imageFetchCallV1) error {
	var pull image.Request
	if !api.ExactJSONFields(request.Args, pull) || json.Unmarshal(request.Args, &pull) != nil || pull.Validate() != nil || pull.Transfer == "" {
		return imageFetchIntentError()
	}
	entry.token = pull.Transfer
	pull.Transfer = ""
	if p.usedHost || p.mode == "laptop" || entry.token == p.installCorrelation || !reflect.DeepEqual(pull, p.resolved.request) {
		return imageFetchIntentError()
	}
	p.usedHost, p.hostToken = true, entry.token
	return nil
}

func (p *imageFetchIntentV1) admitInstall(install image.Install, entry *imageFetchCallV1) error {
	entry.action, entry.token, entry.digest, entry.offset, entry.bytes = install.Action, install.Token, install.Digest, install.Offset, len(install.Data)
	switch install.Action {
	case "begin":
		return p.admitBegin(install, entry)
	case "chunk":
		if install.Request != nil || install.Token == "" || install.Token != p.installToken || len(install.Data) == 0 || len(install.Data) > image.MaxChunk {
			return imageFetchIntentError()
		}
		bound, known := p.blobs[install.Digest]
		if !known || install.Offset != p.offsets[install.Digest] || int64(len(install.Data)) > bound-install.Offset {
			return imageFetchIntentError()
		}
	case "commit":
		if install.Request != nil || install.Token == "" || install.Token != p.installToken || install.Digest != "" || install.Offset != 0 || len(install.Data) != 0 {
			return imageFetchIntentError()
		}
		for digest, bound := range p.blobs {
			if p.offsets[digest] != bound {
				return imageFetchIntentError()
			}
		}
	case "abort":
		controller, alias, known := p.cleanupAliasLocked(install.Token)
		if !known || install.Request != nil || install.Token != alias || install.Digest != "" || install.Offset != 0 || len(install.Data) != 0 {
			return imageFetchIntentError()
		}
		entry.controller = controller
	default:
		return imageFetchIntentError()
	}
	return nil
}

func (p *imageFetchIntentV1) admitBegin(install image.Install, entry *imageFetchCallV1) error {
	if p.usedLaptop || p.blobs == nil || p.mode == "host" || install.Request == nil || install.Token != "" || install.Digest != "" || install.Offset != 0 || len(install.Data) != 0 {
		return imageFetchIntentError()
	}
	original := install.Request.Clone()
	if !supply.IsHex64(original.Transfer+original.Transfer) || original.Transfer == p.hostToken {
		return imageFetchIntentError()
	}
	entry.token = original.Transfer
	original.Transfer = ""
	if !reflect.DeepEqual(original, p.resolved.request) {
		return imageFetchIntentError()
	}
	p.usedLaptop, p.installCorrelation = true, entry.token
	return nil
}

func (p *imageFetchIntentV1) wroteRequest(c *Conn, id uint64) {
	if p == nil || p.conn != c {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if call := p.turns[id]; call != nil {
		call.wrote = true
		if call.action == "chunk" {
			p.offsets[call.digest] += int64(call.bytes)
		}
	}
}

func (p *imageFetchIntentV1) afterResponse(_ context.Context, c *Conn, operation string, id uint64, response proto.Response, _ any) error {
	if p == nil {
		return nil
	}
	if p.conn != c || response.ID != id {
		return imageFetchIntentError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	call := p.turns[id]
	if call == nil {
		return nil
	}
	if call.op != operation {
		return imageFetchIntentError()
	}
	if !call.wrote || call.replied {
		return imageFetchIntentError()
	}
	call.replied = true
	if call.action == "begin" && response.OK {
		var progress image.Progress
		if !api.ExactJSONFields(response.Result, progress) || json.Unmarshal(response.Result, &progress) != nil || !supply.IsHex64(progress.Token+progress.Token) {
			p.uncertain = true
			return imageFetchIntentError()
		}
		p.installToken = progress.Token
	}
	if call.action == "abort" && response.OK {
		p.closedTokens[call.controller] = true
	}
	if (operation == "image.pull" || call.action == "commit") && response.OK {
		var record image.Record
		if !api.ExactJSONFields(response.Result, record) || json.Unmarshal(response.Result, &record) != nil || image.CheckRecord(p.resolved.request, record) != nil {
			p.uncertain = true
			return imageFetchIntentError()
		}
		controller := call.token
		if call.action == "commit" {
			controller = p.installCorrelation
		}
		p.closedTokens[controller] = true
		p.complete = true
	}
	return nil
}

func (p *imageFetchIntentV1) cleanupFrame(ctx context.Context, c *Conn, request proto.Request, install image.Install) error {
	if p == nil || c != p.conn || grantRequestOf(ctx) != p.request || c.hostTransport != p.use.stamp || c.proc != p.use.stamp.proc || c.route.t != p.use.stamp.transport || c.HostID() != p.scope.hostID {
		return imageFetchIntentError()
	}
	if install.Request != nil || install.Token == "" || install.Digest != "" || install.Offset != 0 || len(install.Data) != 0 {
		return imageFetchIntentError()
	}
	cleanup, _ := ctx.Value(imageFetchCleanupKeyV1{}).(*imageFetchAbortV1)
	if cleanup == nil || cleanup.owner != p || install.Token != cleanup.token {
		return imageFetchIntentError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	controller, _, known := p.cleanupAliasLocked(install.Token)
	if !known || controller != cleanup.controller || p.aborts[controller] != cleanup {
		return imageFetchIntentError()
	}
	entry := p.planned
	if entry == nil || entry.op != request.Op || !reflect.DeepEqual(entry.wire, []byte(request.Args)) {
		return imageFetchIntentError()
	}
	entry.id, entry.action, entry.token, entry.controller = request.ID, "abort", install.Token, controller
	p.ops = append(p.ops, entry)
	p.turns[entry.id] = entry
	return nil
}

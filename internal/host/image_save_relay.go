package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeatureImageSave) }

func IsImageSaveOperationV1(op string) bool {
	return op == proto.FeatureImageSave || op == "image.keep" || op == "image.drop" || op == "image.review"
}

type imageSaveRelayV1 struct {
	line     *holderLine
	output   *proto.Writer
	owner    OwnedBridgeOutput
	send     sync.Mutex
	once     sync.Once
	done     chan struct{}
	stopped  func() bool
	stopDone chan struct{}
}

func newImageSaveRelayV1(ctx context.Context, primary string, output *proto.Writer, owner OwnedBridgeOutput) (*imageSaveRelayV1, error) {
	if ctx == nil || ctx.Err() != nil || output == nil || owner == nil {
		return nil, UserError("image_owner", "image save has no original owned bridge output", "Use the complete process host entrypoint.")
	}
	line, hello, err := dialSessionHolder(primary, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if err := requireSupervisorFeatures(hello, proto.FeatureImageSave); err != nil {
		return nil, errors.Join(err, line.c.Close())
	}
	_ = line.c.SetDeadline(time.Time{})
	relay := &imageSaveRelayV1{line: line, output: output, owner: owner, done: make(chan struct{}), stopDone: make(chan struct{})}
	relay.stopped = context.AfterFunc(ctx, func() { defer close(relay.stopDone); relay.once.Do(func() { _ = relay.line.c.Close() }) })
	go relay.read()
	return relay, nil
}
func (relay *imageSaveRelayV1) forward(request proto.Request) error {
	relay.send.Lock()
	defer relay.send.Unlock()
	select {
	case <-relay.done:
		return UserError("image_closed", "the original image-save connection ended", "Reconnect before saving another image.")
	default:
	}
	_ = relay.line.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := relay.line.w.WriteJSON(proto.TRequest, request)
	_ = relay.line.c.SetWriteDeadline(time.Time{})
	if err != nil {
		relay.once.Do(func() { _ = relay.line.c.Close() })
	}
	return err
}
func (relay *imageSaveRelayV1) read() {
	defer close(relay.done)
	defer relay.owner.Cancel()
	defer relay.once.Do(func() { _ = relay.line.c.Close() })
	for {
		frame, err := proto.Read(relay.line.r)
		if err != nil || frame.Type != proto.TResponse {
			return
		}
		// The existing writer owns complete canonical frames and its real output.
		// No request table, save token or private source is minted by this relay.
		if err := relay.output.Write(proto.TResponse, frame.Payload); err != nil {
			_ = relay.owner.Cancel()
			return
		}
	}
}
func (relay *imageSaveRelayV1) Close() error {
	if relay == nil {
		return nil
	}
	_ = relay.owner.Cancel() // unblock physical output before joining its reader
	relay.once.Do(func() { _ = relay.line.c.Close() })
	if relay.stopped != nil && !relay.stopped() {
		<-relay.stopDone
	}
	<-relay.done
	return nil
}

// forwardImageSaveV1 runs on the actual bridge reader, in frame order. The
// exact selected session retains one normal supervisor connection through
// save/keep/drop; a pending token never migrates to a newly dialled runtime.
func (b *bridge) forwardImageSaveV1(request proto.Request) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	var selected string
	if request.Op == proto.FeatureImageSave {
		var args image.SaveArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) || !image.ValidSaveRef(args.Request.Ref) {
			return UserError("image_protocol", "invalid original image-save route", "Use the current client and a complete session address.")
		}
		selected = args.Session
		clear(args.Request.Values)
	} else if request.Op == "image.review" {
		var args image.SaveReviewArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) || args.Offset < 0 {
			return UserError("image_protocol", "invalid original image-review page route", "Use the same original session connection and pending token.")
		}
		selected = args.Session
	} else {
		var args image.SaveSelectionArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) {
			return UserError("image_protocol", "invalid original image-save selection route", "Use the same accepted connection and review token.")
		}
		selected = args.Session
	}
	b.mu.Lock()
	relay := b.imageSaveRelays[selected]
	closed := b.imageSaveClosed
	b.mu.Unlock()
	if closed {
		return UserError("image_closed", "the original image-save bridge ended", "Reconnect before saving another image.")
	}
	if relay == nil {
		if request.Op != proto.FeatureImageSave {
			return UserError("image_protocol", "the image review has no original accepted route", "Save and review the image on this same connection.")
		}
		primary, err := SandboxPrimaryID(selected)
		if err != nil {
			return err
		}
		manifest, err := LoadManifest(primary)
		if err != nil {
			return err
		}
		if manifest.Wall == nil || manifest.Wall.Level != "microvm" {
			return UserError("image_wall", "image save needs a running microvm sandbox", "Start a microvm sandbox, set up its tools, then save its image.")
		}
		if !SessionRunning(manifest) {
			return UserError("sandbox_stopped", "the sandbox is not running", "Resume its microvm before saving its image.")
		}
		created, err := newImageSaveRelayV1(b.ctx, primary, b.w, b.ownedOutput)
		if err != nil {
			return err
		}
		b.mu.Lock()
		if b.imageSaveClosed {
			b.mu.Unlock()
			_ = created.Close()
			return UserError("image_closed", "the image-save bridge ended during admission", "Reconnect and repeat image save.")
		}
		if b.imageSaveRelays == nil {
			b.imageSaveRelays = make(map[string]*imageSaveRelayV1)
		}
		b.imageSaveRelays[selected] = created
		relay = created
		b.mu.Unlock()
	}
	return relay.forward(request)
}
func (b *bridge) closeImageSavesV1() error {
	b.mu.Lock()
	b.imageSaveClosed = true
	relays := b.imageSaveRelays
	b.imageSaveRelays = nil
	b.mu.Unlock()
	var result error
	for _, relay := range relays {
		result = errors.Join(result, relay.Close())
	}
	return result
}

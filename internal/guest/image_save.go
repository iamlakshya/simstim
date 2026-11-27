package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type imageSaveVMRootsV1 interface {
	imageSaveRootsV1() (*os.Root, *os.Root, image.SavedRootPlan, error)
	syncImageSaveRootV1(*os.Root) error
}
type imageSaveGuestV1 struct {
	root, upper *os.Root
	plan        image.SavedRootPlan
	ctx         context.Context
	cancel      context.CancelFunc
	ready       chan struct{}
	mu          sync.Mutex
	paths       []string
	err         error
	claimed     bool
	stream      *link.Stream
	writeDone   chan struct{}
	release     func()
	once        sync.Once
	closeErr    error
}

func imageSaveGuestErrorV1(message string) error {
	return &proto.Error{Code: "image_protocol", Message: message, Hint: "Use the original microvm export on its same current guest link."}
}
func imageSaveIDV1(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

func (g *Init) imageSaveControlV1(request proto.Request, reply func(any, error)) bool {
	if request.Op != proto.CtlImageChanges && request.Op != proto.CtlImageLayerCancel {
		return false
	}
	if request.Op == proto.CtlImageLayerCancel {
		var args proto.ImageLayerCancel
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !imageSaveIDV1(args.ID) {
			reply(nil, imageSaveGuestErrorV1("invalid original image export cancellation"))
			return true
		}
		reply(struct{}{}, g.cancelImageSaveGuestV1(args.ID))
		return true
	}
	var args proto.ImageChangesArgs
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !imageSaveIDV1(args.ID) || args.MaxEntries != proto.ImageChangesMax || args.Offset < 0 || args.Offset > proto.ImageChangesMax {
		reply(nil, imageSaveGuestErrorV1("invalid bounded image changes reservation"))
		return true
	}
	g.mu.Lock()
	operation := g.imageSaves[args.ID]
	ended := g.ended
	g.mu.Unlock()
	if ended {
		reply(nil, imageSaveGuestErrorV1("the original guest is closing"))
		return true
	}
	if operation == nil {
		if args.Offset != 0 {
			reply(nil, imageSaveGuestErrorV1("the original image changes reservation is absent"))
			return true
		}
		var err error
		operation, err = g.beginImageSaveGuestV1(args.ID)
		if err != nil {
			reply(nil, err)
			return true
		}
		go func() {
			paths, err := image.ListSavedChanges(operation.ctx, operation.upper, proto.ImageChangesMax)
			operation.mu.Lock()
			operation.paths, operation.err = paths, err
			operation.mu.Unlock()
			close(operation.ready)
		}()
	}
	go func() { result, err := operation.page(args.ID, args.Offset); reply(result, err) }()
	return true
}
func (g *Init) beginImageSaveGuestV1(id string) (*imageSaveGuestV1, error) {
	original, ok := g.vm.(imageSaveVMRootsV1)
	if !ok || g.vm == nil || !g.vm.prepared() {
		return nil, imageSaveGuestErrorV1("image save has no actual prepared microvm root")
	}
	release, err := g.vm.controlOperation()
	if err != nil {
		return nil, err
	}
	root, upper, plan, err := original.imageSaveRootsV1()
	if err != nil {
		release()
		return nil, err
	}
	ctx, cancel := context.WithCancel(g.lifetime)
	operation := &imageSaveGuestV1{root: root, upper: upper, plan: plan, ctx: ctx, cancel: cancel, ready: make(chan struct{}), release: release}
	g.mu.Lock()
	if g.ended || g.imageSaves[id] != nil {
		g.mu.Unlock()
		cancel()
		_ = root.Close()
		_ = upper.Close()
		release()
		return nil, imageSaveGuestErrorV1("the image export identity was already consumed")
	}
	if g.imageSaves == nil {
		g.imageSaves = make(map[string]*imageSaveGuestV1)
	}
	g.imageSaves[id] = operation
	g.mu.Unlock()
	return operation, nil
}
func (operation *imageSaveGuestV1) page(id string, offset int) (proto.ImageChangesResult, error) {
	<-operation.ready
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if operation.err != nil {
		return proto.ImageChangesResult{}, operation.err
	}
	if err := operation.ctx.Err(); err != nil {
		return proto.ImageChangesResult{}, err
	}
	if offset < 0 || offset > len(operation.paths) {
		return proto.ImageChangesResult{}, imageSaveGuestErrorV1("the image changes offset is outside its original snapshot")
	}
	next, used := offset, 0
	for next < len(operation.paths) {
		encoded, err := json.Marshal(operation.paths[next])
		if err != nil {
			return proto.ImageChangesResult{}, err
		}
		if used+len(encoded)+1 > proto.ImageChangesPageBytes-1024 {
			break
		}
		used += len(encoded) + 1
		next++
	}
	result := proto.ImageChangesResult{Version: 1, ID: id, Paths: slices.Clone(operation.paths[offset:next]), Next: next, Done: next == len(operation.paths)}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > proto.ImageChangesPageBytes || next == offset && !result.Done {
		return proto.ImageChangesResult{}, imageSaveGuestErrorV1("the image changes page exceeds its existing control bound")
	}
	return result, nil
}
func (g *Init) acceptImageSaveLayerV1(stream *link.Stream) {
	var header proto.ImageLayerHeader
	if stream.Header(&header) != nil || !api.ExactJSONFields(stream.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		proto.ImageLayerHeader
	}{}) || header.Version != 1 || !imageSaveIDV1(header.ID) || header.Format != "tar" || header.MaxBytes != image.MaxUncompressed {
		_ = stream.Close()
		return
	}
	g.mu.Lock()
	operation := g.imageSaves[header.ID]
	ended := g.ended
	g.mu.Unlock()
	if operation == nil || ended {
		_ = stream.Close()
		return
	}
	operation.mu.Lock()
	select {
	case <-operation.ready:
	default:
		operation.mu.Unlock()
		_ = stream.Close()
		return
	}
	if operation.claimed || operation.err != nil || operation.ctx.Err() != nil {
		operation.mu.Unlock()
		_ = stream.Close()
		return
	}
	operation.claimed = true
	operation.stream = stream
	operation.writeDone = make(chan struct{})
	operation.mu.Unlock()
	go func() {
		defer close(operation.writeDone)
		stopped := make(chan struct{})
		stop := context.AfterFunc(operation.ctx, func() { defer close(stopped); _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
		original, ok := g.vm.(imageSaveVMRootsV1)
		var err error
		if !ok {
			err = imageSaveGuestErrorV1("the actual original VM export owner disappeared")
		} else {
			err = original.syncImageSaveRootV1(operation.root)
		}
		if err == nil {
			err = image.WriteSavedRoot(operation.ctx, operation.root, operation.plan, stream)
		}
		if err == nil {
			err = stream.CloseWrite()
		} else {
			_ = stream.Close()
		}
		if !stop() {
			<-stopped
		}
		operation.mu.Lock()
		operation.err = errors.Join(operation.err, err)
		operation.mu.Unlock()
	}()
}
func (g *Init) cancelImageSaveGuestV1(id string) error {
	g.mu.Lock()
	operation := g.imageSaves[id]
	g.mu.Unlock()
	if operation == nil {
		return nil
	}
	operation.once.Do(func() {
		operation.cancel()
		<-operation.ready
		operation.mu.Lock()
		stream, writeDone := operation.stream, operation.writeDone
		operation.mu.Unlock()
		if stream != nil {
			_ = stream.SetDeadline(time.Now())
			_ = stream.Close()
		}
		if writeDone != nil {
			<-writeDone
		}
		operation.closeErr = errors.Join(operation.root.Close(), operation.upper.Close())
		g.mu.Lock()
		delete(g.imageSaves, id)
		g.mu.Unlock()
		operation.release()
	})
	return operation.closeErr
}
func (g *Init) stopImageSavesV1() error {
	g.mu.Lock()
	ids := make([]string, 0, len(g.imageSaves))
	for id := range g.imageSaves {
		ids = append(ids, id)
	}
	g.mu.Unlock()
	var result error
	for _, id := range ids {
		result = errors.Join(result, g.cancelImageSaveGuestV1(id))
	}
	return result
}
func (g *Init) imageSaveGuestFeaturesV1(features []string) []string {
	if _, ok := g.vm.(imageSaveVMRootsV1); ok && g.vm != nil && g.vm.prepared() {
		return append(features, proto.FeatureImageSave)
	}
	return features
}

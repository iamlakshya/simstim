package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// imageSaveLayerV1 owns one actual guest reservation on the SAME retained
// native runtime link. It never reopens a saved layout or another connection.
type imageSaveLayerV1 struct {
	root      *Supervisor
	id        string
	reserved  bool
	stream    *link.Stream
	wrote     bool
	closeOnce sync.Once
	closeErr  error
	uncertain bool
}

func newImageSaveLayerV1(root *Supervisor) (*imageSaveLayerV1, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &imageSaveLayerV1{root: root, id: hex.EncodeToString(id[:])}, nil
}
func (source *imageSaveLayerV1) changes(ctx context.Context) ([]string, error) {
	var features proto.CtlGuestFeatures
	if err := source.root.ctl.requestDetached(ctx, proto.CtlGuestCapabilities, struct{}{}, &features); err != nil {
		return nil, err
	}
	found := false
	for _, feature := range features.Features {
		if features.Version == 1 && feature == proto.FeatureImageSave {
			found = true
		}
	}
	if !found {
		return nil, imageSaveFailureV1("the actual original guest does not support image save", nil)
	}
	source.reserved = true // Explicit cancel owns even a reply lost after send.
	changes := []string{}
	total := 0
	offset := 0
	for {
		var raw json.RawMessage
		args := proto.ImageChangesArgs{Version: 1, ID: source.id, MaxEntries: proto.ImageChangesMax, Offset: offset}
		if err := source.root.ctl.requestDetached(ctx, proto.CtlImageChanges, args, &raw); err != nil {
			return nil, err
		}
		var result proto.ImageChangesResult
		if len(raw) > proto.ImageChangesPageBytes || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.ID != source.id || result.Next != offset+len(result.Paths) || result.Next > proto.ImageChangesMax || !result.Done && len(result.Paths) == 0 {
			return nil, imageSaveFailureV1("the original guest returned invalid image changes", nil)
		}
		for _, name := range result.Paths {
			total += len(name)
			if _, err := image.SaveRootPath(name); err != nil || total > proto.ImageChangesBytes {
				return nil, imageSaveFailureV1("the original guest changes exceed their declared path bound", err)
			}
			changes = append(changes, name)
		}
		offset = result.Next
		if result.Done {
			return changes, nil
		}
	}
}
func (source *imageSaveLayerV1) writeRoot(ctx context.Context, output io.Writer) (returned error) {
	if source.wrote || !source.reserved || output == nil {
		return imageSaveFailureV1("the original layer stream was already consumed or unreserved", nil)
	}
	source.wrote = true
	defer func() { returned = errors.Join(returned, source.closeJoined()) }()
	stream, err := source.root.link.Open(ctx, link.KindLayer, proto.ImageLayerHeader{Version: 1, ID: source.id, Format: "tar", MaxBytes: image.MaxUncompressed})
	if err != nil {
		return err
	}
	source.stream = stream
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(stopped); _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	count, err := io.Copy(output, io.LimitReader(stream, image.MaxUncompressed+1))
	if count > image.MaxUncompressed {
		return imageSaveFailureV1("the actual root stream exceeds its declared byte bound", nil)
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

// closeJoined requires the actual guest's same-ID export cancellation ACK.
// A failed ACK retains the source until its SAME original VMM is physically
// joined. Link EOF, a guest claim or metadata cannot satisfy that fallback.
func (source *imageSaveLayerV1) closeJoined() error {
	source.closeOnce.Do(func() {
		if source.stream != nil {
			_ = source.stream.SetDeadline(time.Now())
			source.closeErr = source.stream.Close()
		}
		if !source.reserved {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(source.root.lifetimeContext()), 5*time.Second)
		err := source.root.ctl.requestDetached(cleanup, proto.CtlImageLayerCancel, proto.ImageLayerCancel{Version: 1, ID: source.id}, nil)
		cancel()
		if err != nil {
			physicalErr := source.root.stopImageSaveRuntimeV1()
			source.uncertain = !source.root.imageSavePhysicalExitV1()
			source.closeErr = errors.Join(source.closeErr, err, physicalErr)
		}
	})
	return source.closeErr
}

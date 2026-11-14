package pullexport

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// GuestOptions joins the canonical supervisor's real owned resources to the
// bundle domain. Call and Open must address the same existing guest link.
// Close cancels and joins that owner's resources, releasing its primary lease.
type GuestOptions struct {
	ID, Primary, Cwd string
	Seeds            []string
	PullMax          int64
	Call             func(context.Context, string, any, any) error
	Open             func(context.Context, link.Kind, any) (*link.Stream, error)
	Record           func(context.Context, []string) error
	Close            func() error
}

// GuestSource uses the canonical ExecRecords for a domain-checked guest bundle.
// It owns no process, sandbox factory, operation lease or alternate parser.
type GuestSource struct {
	options GuestOptions
	once    sync.Once
	closed  chan struct{}
	err     error
}

// NewGuestSource validates the exact inner identity before exposing the source.
func NewGuestSource(options GuestOptions) (*GuestSource, error) {
	if !api.ValidSessionID(options.ID) || len(options.ID) != 26 || !api.ValidSessionID(options.Primary) || options.Cwd == "" || options.PullMax <= 0 || options.Call == nil || options.Open == nil || options.Record == nil || options.Close == nil {
		return nil, exportError("pull_protocol", "the owned guest bundle source is incomplete")
	}
	options.Seeds = slices.Clone(options.Seeds)
	for _, seed := range options.Seeds {
		if !api.ValidGitOID(seed) {
			return nil, exportError("pull_protocol", "the protected seed history is invalid")
		}
	}
	return &GuestSource{options: options, closed: make(chan struct{})}, nil
}

// SeedHeads returns protected seed identities, never a guest claim.
func (s *GuestSource) SeedHeads() []string { return slices.Clone(s.options.Seeds) }

// PullMax is the fresh applied cap materialised by the canonical primary owner.
func (s *GuestSource) PullMax() int64 { return s.options.PullMax }

// Capture prepares the fixed exporter then consumes only canonical stdout,
// bounded stderr and a successful terminal result followed by exact EOF.
func (s *GuestSource) Capture(ctx context.Context, args CaptureArgs, out io.Writer) (CaptureResult, error) {
	request := proto.CtlBundleArgs{ID: s.options.ID, Primary: s.options.Primary, Cwd: s.options.Cwd, SeedHeads: slices.Clone(s.options.Seeds), Full: args.Full, DefaultRef: args.DefaultRef}
	if len(args.Heads) != 0 {
		request.HeadCount, request.HeadsDigest = proto.BundleSnapshot(args.Heads)
	}
	if !proto.BundleRequestFits(0, request) {
		return CaptureResult{}, exportError("pull_protocol", "the captured session refs exceed the control metadata bound")
	}
	var ready proto.CtlBundleReady
	if err := s.options.Call(withCaptureProgress(ctx, args.Progress), proto.CtlBundlePrepare, request, &ready); err != nil {
		return CaptureResult{}, err
	}
	if ready.ID != request.ID {
		return CaptureResult{}, exportError("pull_protocol", "the guest changed its bundle identity")
	}
	if !proto.BundleResponseFits(0, ready) {
		return CaptureResult{}, exportError("pull_protocol", "the guest bundle metadata exceeded its bound")
	}
	if ready.Empty {
		if ready.DefaultRef != "" {
			return CaptureResult{}, exportError("pull_protocol", "the guest returned inconsistent empty bundle metadata")
		}
		return CaptureResult{Empty: true}, nil
	}
	if args.Full && !ready.Full {
		return CaptureResult{}, exportError("pull_protocol", "the guest changed the requested full-history mode")
	}
	stream, err := s.options.Open(ctx, link.KindBundle, proto.ExecHeader{ID: s.options.ID, Purpose: "pull"})
	if err != nil {
		return CaptureResult{}, err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.SetDeadline(time.Now()) })
	defer stop()
	// Keep input open after its explicit EOF until output ends. Closing the
	// stream's input early would correctly cancel the owned guest command.
	if err = link.NewExecRecords(stream).CloseInput(); err != nil {
		return CaptureResult{}, err
	}
	diagnostic := &progressDiscard{remaining: 64 << 10, progress: args.Progress}
	code, err := link.ReadExecOutput(stream, out, diagnostic)
	if ctx.Err() != nil {
		return CaptureResult{}, ctx.Err()
	}
	if err != nil {
		return CaptureResult{}, err
	}
	if code != 0 {
		return CaptureResult{}, exportError("pull_capture", fmt.Sprintf("the guest bundle command ended with status %d", code))
	}
	return CaptureResult{DefaultRef: ready.DefaultRef, Full: ready.Full}, nil
}

type captureProgressKey struct{}

func withCaptureProgress(ctx context.Context, progress func()) context.Context {
	return context.WithValue(ctx, captureProgressKey{}, progress)
}

// CaptureProgress exposes the scoped callback only to the canonical ctl
// owner. It may renew liveness while the same pending capture exists and ctx
// is live. It confers no completion, secret or remote producer authority.
func CaptureProgress(ctx context.Context) func() {
	progress, _ := ctx.Value(captureProgressKey{}).(func())
	return func() {
		if progress != nil && ctx.Err() == nil {
			progress()
		}
	}
}

type progressDiscard struct {
	remaining int
	progress  func()
}

func (w *progressDiscard) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, exportError("pull_protocol", "guest Git diagnostics exceeded their bound")
	}
	w.remaining -= len(p)
	if len(p) > 0 && w.progress != nil {
		w.progress()
	}
	return len(p), nil
}

// RecordPulled delegates only exact successfully reviewed commit identities.
func (s *GuestSource) RecordPulled(ctx context.Context, commits []string) error {
	return s.options.Record(ctx, slices.Clone(commits))
}

// Close releases the guest operation before the canonical resource owner joins.
func (s *GuestSource) Close() error {
	s.once.Do(func() {
		defer close(s.closed)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var ignored struct{}
		_ = s.options.Call(ctx, proto.CtlBundleCancel, struct {
			ID string `json:"id"`
		}{s.options.ID}, &ignored)
		s.err = s.options.Close()
	})
	<-s.closed
	return s.err
}

package pullexport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

type exportFixture struct {
	mu            sync.Mutex
	captures      []CaptureArgs
	recorded      []string
	closes        int
	closed        chan struct{}
	empty         bool
	differentFull bool
	capture       func(context.Context, CaptureArgs, io.Writer) (CaptureResult, error)
	cap           *int64
}

func (*exportFixture) SeedHeads() []string { return []string{strings.Repeat("a", 40)} }
func (f *exportFixture) PullMax() int64 {
	if f.cap != nil {
		return *f.cap
	}
	return 1 << 20
}
func (f *exportFixture) Capture(ctx context.Context, args CaptureArgs, out io.Writer) (CaptureResult, error) {
	f.mu.Lock()
	f.captures = append(f.captures, args)
	empty, different := f.empty, f.differentFull
	capture := f.capture
	f.mu.Unlock()
	if capture != nil {
		return capture(ctx, args, out)
	}
	ref := "refs/heads/" + brand.Name + "/primary2/topic"
	if empty {
		return CaptureResult{Empty: true}, nil
	}
	tip := strings.Repeat("b", 40)
	if args.Full && different {
		tip = strings.Repeat("c", 40)
	}
	header := "# v2 git bundle\n"
	if !args.Full {
		header += "-" + strings.Repeat("a", 40) + " seed\n"
	}
	if _, err := io.WriteString(out, header+tip+" "+ref+"\n\nPACKfixture"); err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{DefaultRef: ref}, nil
}

func TestFullHistoryRetryCannotRepeatCapture(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	first := beginFixture(t, manager, strings.Repeat("a", 26))
	if _, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	full, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: first.ID, Token: strings.Repeat("b", 26), Previous: first.Token, Full: true}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: full.ID, Token: full.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareBegin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: full.ID, Token: strings.Repeat("c", 26), Previous: full.Token, Full: true}, 1<<20); err == nil {
		t.Fatal("a full export was captured repeatedly through begin")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.captures) != 2 {
		t.Fatal("repeated begin launched another capture")
	}
}
func (f *exportFixture) RecordPulled(ctx context.Context, commits []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append([]string(nil), commits...)
	return nil
}
func (f *exportFixture) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	if f.closes == 1 {
		close(f.closed)
	}
	return nil
}

type fixtureClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixtureClock) read() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fixtureClock) advance(value time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(value)
	c.mu.Unlock()
}

func managerFixture(t *testing.T, source *exportFixture) (*Manager, *fixtureClock, chan time.Time) {
	t.Helper()
	directory := t.TempDir()
	clock := &fixtureClock{now: time.Unix(1000, 0)}
	ticks := make(chan time.Time, 8)
	manager, err := New(t.Context(), Options{Directory: func(primary string) (string, error) {
		path := filepath.Join(directory, primary)
		return path, os.MkdirAll(path, 0700)
	}, Open: func(context.Context, string, string) (Source, error) { return source, nil }, Idle: 2 * time.Minute, Now: clock.read, Ticks: ticks})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, clock, ticks
}
func beginFixture(t *testing.T, manager *Manager, token string) api.PullExport {
	t.Helper()
	descriptor, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: "fixture2", Token: token}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestExportRetainsOneOwnerThroughFullRetryAndExactAck(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	first := beginFixture(t, manager, strings.Repeat("a", 26))
	if _, err := manager.Read(t.Context(), "another-owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err == nil {
		t.Fatal("token spelling donated another owner's export")
	}
	chunk, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: first.ID, Token: first.Token})
	if err != nil || !chunk.EOF {
		t.Fatal(err)
	}
	full, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: first.ID, Token: strings.Repeat("b", 26), Previous: first.Token, Full: true}, 1<<20)
	if err != nil || !full.Full {
		t.Fatal(err)
	}
	source.mu.Lock()
	sameOwner := source.closes == 0 && len(source.captures) == 2 && len(source.captures[1].Heads) == 1
	source.mu.Unlock()
	if !sameOwner {
		t.Fatal("full retry did not retain the same owned resource and snapshot")
	}
	if _, err := manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: full.ID, Token: full.Token, Commits: []string{strings.Repeat("f", 40)}}); err == nil {
		t.Fatal("uncaptured commit was acknowledged")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.recorded) != 0 || source.closes != 1 {
		t.Fatal("invalid ack wrote state or leaked the owner")
	}
}

func TestExportIdleClockRequiresProgressAndExactEOFKeepsItAlive(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, clock, ticks := managerFixture(t, source)
	descriptor := beginFixture(t, manager, strings.Repeat("a", 26))
	chunk, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: descriptor.ID, Token: descriptor.Token})
	if err != nil || !chunk.EOF {
		t.Fatal(err)
	}
	for range 3 {
		clock.advance(time.Minute)
		eof, err := manager.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: descriptor.ID, Token: descriptor.Token, Offset: descriptor.Size})
		if err != nil || len(eof.Data) != 0 || !eof.EOF {
			t.Fatal("exact EOF did not renew the same owner", err)
		}
		ticks <- clock.read()
	}
	select {
	case <-source.closed:
		t.Fatal("healthy local review was treated as a hard lifetime expiry")
	default:
	}
	clock.advance(3 * time.Minute)
	ticks <- clock.read()
	select {
	case <-source.closed:
	case <-time.After(time.Second):
		t.Fatal("lost client did not expire and join its own resource")
	}
}

func TestEmptyExportReleasesImmediatelyAndMalformedReadsDoNotAdvance(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{}), empty: true}
	manager, _, _ := managerFixture(t, source)
	descriptor := beginFixture(t, manager, strings.Repeat("a", 26))
	if !descriptor.Empty {
		t.Fatal("empty guest was not represented")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("empty export retained its primary lease")
	}
	if _, err := manager.Finish(t.Context(), "outer-owner", api.PulledArgs{ID: descriptor.ID, Token: descriptor.Token}); err != nil {
		t.Fatal(err)
	}
}

func TestExportCapAndFullSnapshotMismatchCloseOwnedResources(t *testing.T) {
	source := &exportFixture{closed: make(chan struct{})}
	manager, _, _ := managerFixture(t, source)
	if _, err := manager.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}, 4); err == nil {
		t.Fatal("oversized stream was published")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("cap failure leaked owned source")
	}
	source2 := &exportFixture{closed: make(chan struct{}), differentFull: true}
	manager2, _, _ := managerFixture(t, source2)
	first := beginFixture(t, manager2, strings.Repeat("a", 26))
	if _, err := manager2.Read(t.Context(), "outer-owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager2.Begin(t.Context(), "outer-owner", "primary2", api.PullBeginArgs{ID: first.ID, Token: strings.Repeat("b", 26), Previous: first.Token, Full: true}, 1<<20); err == nil {
		t.Fatal("full retry silently changed captured heads")
	}
}

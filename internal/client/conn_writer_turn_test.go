package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

type queuedSourceFrameSink struct {
	once     sync.Once
	entered  chan struct{}
	release  chan struct{}
	requests atomic.Int32
}

func (s *queuedSourceFrameSink) Write(frame []byte) (int, error) {
	if len(frame) > 4 && proto.Type(frame[4]) == proto.TPing {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	if len(frame) > 4 && proto.Type(frame[4]) == proto.TRequest {
		s.requests.Add(1)
	}
	return len(frame), nil
}

func TestActualQueuedResumeWriterRefusesChangedOriginalSettings(t *testing.T) {
	c, _, ctx, creation := resumeImageFixture(t, true, "")
	ctx, proof, err := captureResumeImagePolicy(ctx, c, creation.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, args, err := proof.arguments(ctx, ResumeArgs{ID: creation.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	sink := &queuedSourceFrameSink{entered: make(chan struct{}), release: make(chan struct{})}
	c.w = proto.NewWriter(sink)
	c.pending = make(map[uint64]chan proto.Response)
	pingDone := make(chan error, 1)
	go func() { pingDone <- c.w.Write(proto.TPing, nil) }()
	<-sink.entered
	queued := make(chan struct{})
	before := requestStage
	requestStage = func(stage string) {
		if stage == "queued" {
			close(queued)
		}
	}
	t.Cleanup(func() { requestStage = before })
	owned, release, err := c.beginGrant(ctx, "queued original image fixture", false)
	if err != nil {
		close(sink.release)
		<-pingDone
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() { _, _, err := c.startCall(owned, "resume", args); done <- err }()
	<-queued
	writeRetainedImagePolicyFixture(t, "# changed while the writer turn was occupied\n")
	close(sink.release)
	if err := <-pingDone; err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, errAgentSettingsChanged) {
		t.Fatal("queued settings refusal was lost or remapped", err)
	}
	if sink.requests.Load() != 0 {
		t.Fatal("changed original settings reached the physical sink")
	}
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatal("refused queued frame retained response ownership", pending)
	}
}

func TestOriginalStartSettingsGuardDoesNotOpenOrRenewCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := revalidateOriginalStartSettingsV1(ctx, "image.drop"); err != nil {
		t.Fatal("fixed cleanup acquired a generic start gate", err)
	}
	if err := revalidateOriginalStartSettingsV1(context.Background(), "new"); err != nil {
		t.Fatal("an absent original source was fabricated", err)
	}
}

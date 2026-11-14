package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStagePinCancellationKeepsPreviousStage(t *testing.T) {
	sshDirs(t)
	const name = "box"
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := StagePin(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { AbortPin(name) })
	previous := currentStage(name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := beforeStagePublish
	beforeStagePublish = cancel
	defer func() { beforeStagePublish = old }()
	if err := StagePin(ctx, name); !errors.Is(err, context.Canceled) {
		t.Fatalf("StagePin = %v, want cancellation", err)
	}
	if got := currentStage(name); got != previous {
		t.Fatalf("cancellation replaced the previous stage: %p, want %p", got, previous)
	}
	if _, err := os.Stat(previous.path); err != nil {
		t.Fatalf("previous stage was removed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(sshDir(), ".known_hosts-"+HostFileKey(name)+"-staged-*"))
	if err != nil || len(matches) != 1 || matches[0] != previous.path {
		t.Fatalf("cancellation left staging files: %v, %v", matches, err)
	}
}

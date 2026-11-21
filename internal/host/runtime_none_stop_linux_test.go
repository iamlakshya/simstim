//go:build linux

package host_test

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestNoneActualCompletionStopsOnlyThroughSameOriginalOperation(t *testing.T) {
	hosttest.New(t)
	s := start(t, "none-completion", "/bin/sh", "-c", "printf NONE-COMPLETION; exec sleep 60")
	waitOutput(t, s.ID, "NONE-COMPLETION")
	operation, err := host.BeginSessionOperation(context.Background(), s.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	stopped, err := host.StopNoneSessionForOperationV1(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	defer stopped.Close()
	if err := stopped.Check(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if err := stopped.Check(context.Background(), nil); err == nil {
		t.Fatal("the none completion detached from its original OP")
	}
	if _, wholeUnit := any(stopped).(*host.StoppedSessionV1); wholeUnit {
		t.Fatal("N5 completion became whole-unit stopped authority")
	}
	current, err := host.LoadManifest(s.ID)
	if err != nil || !host.IsNoneSupervisor(current) || current.Run.Generation == "" {
		t.Fatal("the actual none controller did not preserve its final trusted record", err)
	}
	// This fixture owns only its temporary metadata. Native closure was
	// positively checked above; no stopped DTO is used as a cleanup source.
	if err := os.RemoveAll(paths.SessionDir(s.ID)); err != nil {
		t.Fatal(err)
	}
}

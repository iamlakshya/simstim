package supervisor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestDiskWatchRequestsOwnedStopOnMeasurementFailure(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(60, 0)
	var stopped error
	err = watchDisk(t.Context(), diskOptions{volume: root, roots: []*os.Root{root}, process: true, limit: 1024, ticks: ticks,
		space: func(*os.Root) (files.Space, error) { return files.Space{Available: 50, Total: 100}, nil },
		measure: func(context.Context, ...*os.Root) (files.Usage, error) {
			return files.Usage{}, errors.New("fixture EIO")
		},
		requestStop: func(cause error) { stopped = cause }})
	var failure *proto.Error
	if err == nil || !errors.As(stopped, &failure) || failure.Code != "disk_measure" {
		t.Fatalf("failure did not request an honest owned stop: %v", err)
	}
}

func TestMicroVMChecksHostFreeSpaceWithoutChargingSparseDiskLength(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	options := diskOptions{volume: root, process: false, limit: 1,
		space: func(*os.Root) (files.Space, error) { return files.Space{Available: 5, Total: 100}, nil },
		measure: func(context.Context, ...*os.Root) (files.Usage, error) {
			t.Fatal("microvm's sparse virtual size was charged as process disk usage")
			return files.Usage{}, nil
		}}
	if err = checkDisk(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	options.space = func(*os.Root) (files.Space, error) { return files.Space{Available: 4, Total: 100}, nil }
	if err = checkDisk(t.Context(), options); err == nil {
		t.Fatal("low host free space was ignored")
	}
}

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
)

func TestDiskCloseRetainsNativeDescriptorErrorAndStartFence(t *testing.T) {
	volume, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resource := &diskResourcesV1{volume: volume}
	if err := volume.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := resource.Close(); err == nil {
			t.Fatal("an original close error became cleanup success")
		}
	}
	if resource.volume != volume || resource.closed || !resource.closing {
		t.Fatal("failed actual owner or Start fence was discarded")
	}
	if _, err := resource.options(func(error) {}); err == nil {
		t.Fatal("closing owner issued new disk observation")
	}
	called := false
	if _, err := resource.start(context.Background(), func(context.Context) (net.Conn, error) { called = true; return nil, nil }); err == nil || called {
		t.Fatal("failed cleanup granted new Start")
	}
}

func TestDiskCloseRetriesOnlySameActualReleaseOwner(t *testing.T) {
	volume, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	busy := errors.New("actual owner still retained")
	attempts := 0
	resource := &diskResourcesV1{volume: volume, release: func() error {
		attempts++
		if attempts == 1 {
			return busy
		}
		return nil
	}}
	if err := resource.Close(); !errors.Is(err, busy) {
		t.Fatal(err)
	}
	if resource.volume != volume || resource.closed {
		t.Fatal("failed release discarded actual metadata")
	}
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || !resource.closed {
		t.Fatal("same actual release was not retried")
	}
}

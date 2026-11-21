//go:build linux

package none_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

func TestMain(m *testing.M) { hosttest.Main(m) }

func TestActualNoneCreationRejectsCopiedOwnerAndSubstitutedConnection(t *testing.T) {
	hosttest.New(t)
	id := "a2a2a2a2"
	if err := os.MkdirAll(paths.SessionDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := (none.Driver{}).Prepare(context.Background(), wall.Spec{ID: id, SessionDir: paths.SessionDir(id)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := runtime.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		connection.Close()
		if err := runtime.Kill(); err != nil {
			t.Error(err)
		}
	})
	owner, err := none.NativeLiveOwnerV1(runtime, connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.CheckSessionV1(context.Background(), id, connection); err != nil {
		t.Fatal(err)
	}
	copy := *owner
	if err := copy.Check(context.Background()); err == nil {
		t.Fatal("a copied live-owner value substituted for the original creator")
	}
	if err := owner.CheckSessionV1(context.Background(), "b3b3b3b3", connection); err == nil {
		t.Fatal("another session acquired the actual creator")
	}
	if err := owner.CheckSessionV1(context.Background(), id, nil); err == nil {
		t.Fatal("a replacement connection acquired the actual creator")
	}
	join, err := owner.PrepareJoinV1(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer join.Close()
	if err := join.CheckJoinedV1(context.Background()); err == nil {
		t.Fatal("a still-running original init was declared joined")
	}
	connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := runtime.Stop(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := join.CheckJoinedV1(context.Background()); err != nil {
		t.Fatal(err)
	}
}

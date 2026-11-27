//go:build linux

package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
)

func snapshotRepository(data []byte) api.InputRepository {
	sum := sha256.Sum256(data)
	return api.InputRepository{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestInputSnapshotSurvivesSameInodeMutationAndPathReplacement(t *testing.T) {
	original := []byte("the authenticated bundle bytes")
	name := filepath.Join(t.TempDir(), "bundle-0")
	if err := os.WriteFile(name, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	sealed, err := sealInputBundle(t.Context(), source, snapshotRepository(original), api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	if err := os.WriteFile(name, bytes.Repeat([]byte("x"), len(original)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, name+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("a replacement inode"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(sealed)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("sealed bytes changed: %q, %v", got, err)
	}
	if _, err := sealed.WriteAt([]byte("!"), 0); err == nil {
		t.Fatal("sealed input remained writable")
	}
	if err := sealed.Truncate(0); err == nil {
		t.Fatal("sealed input remained shrinkable")
	}
	if err := sealed.Truncate(int64(len(original) + 1)); err == nil {
		t.Fatal("sealed input remained growable")
	}
	seals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&inputSeals != inputSeals {
		t.Fatalf("missing immutable seals: %#x, %v", seals, err)
	}
}

func TestInputSnapshotRejectsChangedBytesAndCancelledOwner(t *testing.T) {
	data := []byte("authenticated")
	name := filepath.Join(t.TempDir(), "bundle")
	if err := os.WriteFile(name, []byte("unauthorised!"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if sealed, err := sealInputBundle(t.Context(), source, snapshotRepository(data), api.DefaultSeedMax); err == nil || sealed != nil {
		t.Fatal("a mismatching bundle produced an authorised descriptor")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if sealed, err := sealInputBundle(ctx, source, snapshotRepository(data), api.DefaultSeedMax); err == nil || sealed != nil {
		t.Fatal("a cancelled owner retained a descriptor")
	}
}

func TestInputSnapshotConcurrentAgentWritesCannotDonateUncheckedBytes(t *testing.T) {
	data := bytes.Repeat([]byte("original bundle block\n"), 8192)
	name := filepath.Join(t.TempDir(), "bundle")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	writer, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	stop := make(chan struct{})
	var joined sync.WaitGroup
	joined.Add(1)
	go func() {
		defer joined.Done()
		mutated := bytes.Repeat([]byte("x"), len(data))
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = writer.WriteAt(mutated, 0)
			_, _ = writer.WriteAt(data, 0)
		}
	}()
	sealed, err := sealInputBundle(t.Context(), source, snapshotRepository(data), api.DefaultSeedMax)
	close(stop)
	joined.Wait()
	if err != nil {
		if sealed != nil {
			t.Fatal("failed snapshot retained a descriptor")
		}
		return
	}
	defer sealed.Close()
	got, err := io.ReadAll(sealed)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("racing agent writes donated bytes outside the protected hash")
	}
}

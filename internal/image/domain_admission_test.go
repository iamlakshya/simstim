package image

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestImageHostLeaseCreationAndTransferDirectoryRespectLaterDrainGate(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	denied := errors.New("fixture placement is draining")
	store.gate = func(context.Context, func() error) error { return denied }
	digest := "sha256:" + strings.Repeat("c", 64)
	if _, err := store.Acquire(t.Context(), digest); !errors.Is(err, denied) {
		t.Fatalf("post-constructor lease bypassed current admission: %v", err)
	}
	if _, err := store.root.Lstat(digest[7:] + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("drain refusal created a lease inode")
	}
	store.gate = imageDomainGate
	lease, err := store.Acquire(t.Context(), digest)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	store.gate = func(context.Context, func() error) error { return denied }
	request := Request{Ref: "registry.example/team/base:stable", Digest: digest, PlatformDigest: digest, Arch: "amd64"}
	if _, err := store.BeginContext(t.Context(), request); !errors.Is(err, denied) {
		t.Fatalf("existing platform lease bypassed transfer creation gate: %v", err)
	}
	root, err := store.childRoot("incoming")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil || len(names) != 0 {
		t.Fatalf("denied transfer left a token directory: %v, %v", names, err)
	}
}

func TestImageHostPopulatedObjectCannotMintReplacementLease(t *testing.T) {
	for _, removeReceipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded-old-inode", true: "unreceipted-legacy"}[removeReceipt], func(t *testing.T) {
			imageDomainHome(t)
			local := imageStore(t)
			request, _ := imageDomainPublish(t, local, "c")
			name := request.PlatformDigest[7:] + ".lock"
			if err := local.root.Remove(name); err != nil {
				t.Fatal(err)
			}
			if removeReceipt {
				if err := local.root.Remove("ownership/" + leaseReceiptName(name)); err != nil {
					t.Fatal(err)
				}
			}
			store, err := OpenHost(t.Context(), local.root.Name(), imageDomainFixture("a", "b"), imageDomainGate)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, err = store.Acquire(t.Context(), request.PlatformDigest)
			requireImageDomainError(t, err)
			if _, err := store.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("populated object acquired a new lease generation")
			}
		})
	}
}

type imageDomainWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *imageDomainWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestImageHostWaiterRechecksActualLeaseNameAfterNativeWait(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	digest := "sha256:" + strings.Repeat("c", 64)
	lease, err := store.lock(t.Context(), digest[7:]+".lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &imageDomainWaitContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		acquired, err := store.Acquire(observed, digest)
		if acquired != nil {
			acquired.Close()
		}
		done <- err
	}()
	// Done is requested by the real contended TryLock wait select, rather
	// than a sleep or a mirrored implementation invocation count.
	<-observed.waiting
	name := digest[7:] + ".lock"
	if err := store.root.Rename(name, name+".foreign"); err != nil {
		t.Fatal(err)
	}
	file, err := store.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	lease.Close()
	if err := <-done; err == nil {
		t.Fatal("waiter acquired authority over a replacement lease name")
	}
	if _, err := store.root.Lstat(name + ".foreign"); err != nil {
		t.Fatal("waiter cleaned another native lease generation")
	}
}

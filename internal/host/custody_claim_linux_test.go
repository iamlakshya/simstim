//go:build linux

package host

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

// These are real kernel primitive oracles. They do not create a native
// manager Source, session grant, current request or execution permission.
func TestCustodyClaimAliasesCanConsumeOnlyOneKernelPacket(t *testing.T) {
	claim, err := newCustodyClaimV1()
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	for range 3 {
		if err := claim.peek(); err != nil {
			t.Fatal("passive inspection consumed the claim", err)
		}
	}
	fd, err := unix.FcntlInt(claim.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	alias := &custodyClaimV1{file: os.NewFile(uintptr(fd), "same-kernel-claim-alias")}
	defer alias.Close()
	var successes atomic.Int32
	var workers sync.WaitGroup
	for _, owner := range []*custodyClaimV1{claim, alias} {
		workers.Go(func() {
			if owner.consume() == nil {
				successes.Add(1)
			}
		})
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatal("duplicated custody aliases issued more or fewer than one claim", successes.Load())
	}
	if claim.peek() == nil || alias.consume() == nil {
		t.Fatal("a consumed kernel queue became reusable")
	}
	if _, err := unix.SendmsgN(int(alias.file.Fd()), []byte{1}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err == nil {
		t.Fatal("a claim alias refilled its closed peer")
	}
}

func TestCustodyClaimRejectsLivePeerAndWrongKernelSocket(t *testing.T) {
	for _, kind := range []int{unix.SOCK_SEQPACKET, unix.SOCK_STREAM} {
		pair, err := unix.Socketpair(unix.AF_UNIX, kind|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		claim := &custodyClaimV1{file: os.NewFile(uintptr(pair[0]), "not-yet-a-custody-claim")}
		peer := os.NewFile(uintptr(pair[1]), "untrusted-live-claim-peer")
		if _, err := unix.SendmsgN(int(peer.Fd()), []byte{1}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err != nil {
			t.Fatal(err)
		}
		if kind == unix.SOCK_STREAM {
			_ = peer.Close()
		}
		if claim.peek() == nil {
			t.Fatal("a live refillable peer or stream socket issued custody")
		}
		_ = peer.Close()
		_ = claim.Close()
	}
}

package guest

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFreezeLeaseRenewsWithoutFreezingTwice(t *testing.T) {
	var mu sync.Mutex
	var changes []bool
	f := &filesystemFreezeV1{change: func(frozen bool) error { mu.Lock(); defer mu.Unlock(); changes = append(changes, frozen); return nil }}
	if err := f.renew(5000); err != nil {
		t.Fatal(err)
	}
	if err := f.renew(5000); err != nil {
		t.Fatal(err)
	}
	if err := f.thaw(); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 2 || !changes[0] || changes[1] {
		t.Fatalf("freeze/thaw changes %v", changes)
	}
	if err := f.renew(5000); err == nil {
		t.Fatal("closed mount accepted renewal")
	}
}

func TestFreezeLeaseExpiresWithoutSupervisor(t *testing.T) {
	thawed := make(chan struct{}, 1)
	f := &filesystemFreezeV1{change: func(frozen bool) error {
		if !frozen {
			thawed <- struct{}{}
		}
		return nil
	}}
	defer func() {
		if err := f.close(); err != nil {
			t.Error(err)
		}
	}()
	if err := f.renew(10); err != nil {
		t.Fatal(err)
	}
	select {
	case <-thawed:
	case <-time.After(time.Second):
		t.Fatal("lost supervisor left mount frozen")
	}
}

func TestFreezeStaleExpiryCannotThawRenewedLease(t *testing.T) {
	thaws := 0
	f := &filesystemFreezeV1{change: func(frozen bool) error {
		if !frozen {
			thaws++
		}
		return nil
	}}
	if err := f.renew(5000); err != nil {
		t.Fatal(err)
	}
	old := f.generation
	if err := f.renew(5000); err != nil {
		t.Fatal(err)
	}
	f.expire(old)
	if thaws != 0 {
		t.Fatal("an old expiry thawed the renewed mount")
	}
	if err := f.close(); err != nil {
		t.Fatal(err)
	}
}

func TestFreezeFailureRetainsOriginalMountForThaw(t *testing.T) {
	fail := true
	f := &filesystemFreezeV1{change: func(frozen bool) error {
		if !frozen && fail {
			return errors.New("kernel refused thaw")
		}
		return nil
	}}
	if err := f.renew(5000); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); err == nil {
		t.Fatal("failed thaw reported closed")
	}
	if f.closed || !f.frozen {
		t.Fatal("failed thaw released its original mount")
	}
	fail = false
	if err := f.close(); err != nil {
		t.Fatal(err)
	}
}

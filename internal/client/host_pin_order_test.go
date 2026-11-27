package client

import (
	"context"
	"os"
	"testing"
)

func TestHostPinCanonicalEntryRefusesMembershipAndExpiredCarriersBeforeOpen(t *testing.T) {
	stateFDTestHome(t)
	name := "box"
	var expired context.Context
	if err := withHostMembershipContext(t.Context(), func(locked context.Context) error {
		expired = context.WithoutCancel(locked)
		for _, exclusive := range []bool{false, true} {
			if unlock, err := lockPinAs(locked, name, exclusive); err == nil {
				unlock()
				t.Fatal("canonical pin acquisition inverted the actual membership lease")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockPinAs(expired, name, true); err == nil {
		unlock()
		t.Fatal("an expired membership carrier started pin acquisition")
	}
	if _, err := os.Lstat(lockFile(name)); !os.IsNotExist(err) {
		t.Fatalf("the refused canonical pin path created a lock file: %v", err)
	}
}

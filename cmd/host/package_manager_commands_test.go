package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestActualPackageManagerHelperDispatchUsesTheClosedFixedAccountEntry(t *testing.T) {
	run := commands[guest.PackageManagerHelperMode]
	if run == nil || reflect.ValueOf(run).Pointer() != reflect.ValueOf(guest.RunPackageManagerHelper).Pointer() {
		t.Fatal("the actual helper command was not registered")
	}
	var detail *proto.Error
	if err := run([]string{"caller-path"}); !errors.As(err, &detail) || detail.Hint == "" {
		t.Fatal("the private helper dispatch accepted caller arguments", err)
	}
}

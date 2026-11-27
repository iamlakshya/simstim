package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestEgressChildCommandIsRegisteredAndValidatesBeforeOpeningInheritedFDs(t *testing.T) {
	if commands["egress"] == nil || reflect.ValueOf(commands["egress"]).Pointer() != reflect.ValueOf(runEgress).Pointer() {
		t.Fatal("supervisor egress child has no real entrypoint")
	}
	for _, args := range [][]string{nil, {""}, {"../session"}, {"valid234567", "extra"}} {
		var detail *proto.Error
		if err := commands["egress"](args); !errors.As(err, &detail) || detail.Code != "bad_request" || detail.Hint == "" {
			t.Fatal("invalid child arguments reached the egress process or lost their hint")
		}
	}
}

package main

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestWallInitCommandUsesVersionedGuestParser(t *testing.T) {
	run := commands["wall-init"]
	if run == nil {
		t.Fatal("the helper cannot enter the guest init")
	}
	// Refuse before touching inherited descriptors or guest kernel state.
	// Real self-test and microvm modes need their actual guest environment.
	err := run([]string{"--unknown-guest-version"})
	var failure *proto.Error
	if !errors.As(err, &failure) || failure.Code != "wall_init" || failure.Hint == "" {
		t.Fatal("the private command lost the guest parser's safe refusal", err)
	}
}

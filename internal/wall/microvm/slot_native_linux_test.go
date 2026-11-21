//go:build linux

package microvm

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSeparateAccountSelectionNeverMigratesExistingDiskMode(t *testing.T) {
	facts := api.CapProbe{RootSteps: []string{"launcher", "slots"}}
	if slotUIDModeV1(facts) != slotUIDModeV1Name {
		t.Fatal("complete explicit root steps did not select the new preparation mode")
	}
	if !savedUIDModeV1(facts, "login") {
		t.Fatal("opting in changed the identity of an existing login-owned disk")
	}
	if !savedUIDModeV1(facts, slotUIDModeV1Name) {
		t.Fatal("the saved own-account mode was refused")
	}
	facts.RootSteps = []string{"launcher"}
	if slotUIDModeV1(facts) != "login" || savedUIDModeV1(facts, slotUIDModeV1Name) {
		t.Fatal("partial root steps promoted or downgraded a saved account mode")
	}
	facts.RootSteps = nil
	if savedUIDModeV1(facts, slotUIDModeV1Name) {
		t.Fatal("missing launcher silently fell back to the login account")
	}
}

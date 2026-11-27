package alerts

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestGuestAlertKeepsClaimAndExitOriginInEveryEncoding(t *testing.T) {
	code := 3
	ev := prepare(Event{Event: Failed, Session: Session{ID: "abcd2345", Name: "job"}, ExitCode: &code, ExitFrom: FromGuest, Claimed: true, Detail: "Proceed?", DetailFrom: FromGuest}, "box", &Settings{Detail: true})
	if !strings.HasSuffix(ev.Summary, "(claimed)") || !strings.Contains(ev.Summary, "claimed exit 3") {
		t.Fatalf("guest exit became a fact: %q", ev.Summary)
	}
	var decoded Event
	if err := json.Unmarshal(ev.JSON(), &decoded); err != nil || !decoded.Claimed || decoded.ExitFrom != FromGuest || decoded.DetailFrom != FromGuest {
		t.Fatalf("guest provenance JSON=%s err=%v", ev.JSON(), err)
	}
	for _, want := range []string{brand.Env("CLAIMED") + "=1", brand.Env("EXIT_FROM") + "=guest", brand.Env("DETAIL_FROM") + "=guest"} {
		if !strings.Contains(strings.Join(ev.env(), "\n"), want) {
			t.Errorf("guest command environment lost %q", want)
		}
	}
}

func TestNativeMemoryFactDoesNotInheritGuestExitClaim(t *testing.T) {
	code := 137
	ev := prepare(Event{Event: OOM, ExitCode: &code, ExitFrom: FromGuest, Session: Session{Name: "job"}}, "box", &Settings{})
	if ev.Claimed || strings.Contains(ev.Summary, "claimed") || ev.ExitFrom != FromGuest {
		t.Fatalf("native memory fact or guest exit origin changed: %+v", ev)
	}
}

func TestDisabledDetailDropsItsGuestSourceFromAllEncodings(t *testing.T) {
	ev := prepare(Event{Event: NeedsInput, Claimed: true, Detail: "private question", DetailFrom: FromGuest}, "box", &Settings{})
	if ev.Detail != "" || ev.DetailFrom != "" || strings.Contains(string(ev.JSON()), "detail_from") || strings.Contains(strings.Join(ev.env(), "\n"), "DETAIL_FROM") {
		t.Fatalf("disabled detail retained guest payload/provenance: %+v", ev)
	}
}

func TestSandboxAndProtectionEventsUseSpecifiedDefaults(t *testing.T) {
	for _, kind := range []string{SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered, DiskFull, LinkSilent} {
		if !slices.Contains(Kinds, kind) || !slices.Contains(DefaultEvents, kind) {
			t.Errorf("%s missing from kinds/defaults", kind)
		}
	}
}

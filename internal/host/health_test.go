package host

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostHealthUsesKnownReadingsAndExactThresholds(t *testing.T) {
	for _, test := range []struct {
		name  string
		facts api.HostFacts
		read  hostHealthReadingsV1
		want  map[string]string
	}{
		{name: "unknown readings", facts: api.HostFacts{DiskFree: 0, PressureMem: 100, Drain: true}, read: hostHealthReadingsV1{diskTotal: 1000, largestImage: 100}, want: map[string]string{"drain-state": "blocks_new"}},
		{name: "below one image", facts: api.HostFacts{DiskKnown: true, DiskFree: 99}, read: hostHealthReadingsV1{diskTotal: 1000, largestImage: 100}, want: map[string]string{"disk-low": "blocks_new"}},
		{name: "one image", facts: api.HostFacts{DiskKnown: true, DiskFree: 100}, read: hostHealthReadingsV1{diskTotal: 1000, largestImage: 100}, want: map[string]string{"disk-low": "warn"}},
		{name: "two images and ten per cent", facts: api.HostFacts{DiskKnown: true, DiskFree: 200}, read: hostHealthReadingsV1{diskTotal: 2000, largestImage: 100}, want: map[string]string{}},
		{name: "pressure threshold", facts: api.HostFacts{PressureMemKnown: true, PressureMem: 10}, want: map[string]string{"memory-pressure": "warn"}},
		{name: "drain", facts: api.HostFacts{DrainKnown: true, Drain: true}, want: map[string]string{"drain": "blocks_new"}},
		{name: "native maintenance", read: hostHealthReadingsV1{reboot: true, lingerKnown: true, crashed: true}, want: map[string]string{"reboot": "warn", "linger": "warn", "vm-crashed": "warn"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.facts.At = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
			if test.name != "unknown readings" {
				test.facts.DrainKnown = true
			}
			items := hostHealthItemsV1(test.facts, test.read)
			if len(items) != len(test.want) {
				t.Fatalf("health = %+v, want %v", items, test.want)
			}
			for _, item := range items {
				if test.want[item.ID] != item.Severity || item.Message == "" || item.Hint == "" {
					t.Fatal("health lost its threshold, message or hint", item)
				}
			}
		})
	}
}

func TestVMCrashHealthUsesTrustedLifeRatherThanGuestOrBootError(t *testing.T) {
	now := time.Now().UTC()
	manifest := Manifest{Wall: &WallRecord{Level: "microvm"}, Status: Status{State: StateErrored, Since: now}}
	if recentVMCrashV1(&manifest, now) {
		t.Fatal("an agent status manufactured a VMM crash")
	}
	manifest.Life = &Life{Phase: LifeEnded, End: &LifeEnd{Kind: EndSandbox, At: now}}
	if !recentVMCrashV1(&manifest, now) {
		t.Fatal("the actual recent sandbox end was omitted")
	}
	manifest.Life.End.Kind = EndBoot
	if recentVMCrashV1(&manifest, now) {
		t.Fatal("a failed pre-boot preparation manufactured a VMM crash")
	}
	manifest.Life.End.Kind, manifest.Life.End.Claimed = EndSandbox, true
	if recentVMCrashV1(&manifest, now) {
		t.Fatal("a guest claim manufactured a VMM crash")
	}
}

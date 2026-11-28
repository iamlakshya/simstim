package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestSupervisorRecordsMemClaims(t *testing.T) {
	raw, _ := json.Marshal(guest.Event{
		Type:         "mem",
		MemUsed:      128 << 20,
		MemTotal:     512 << 20,
		MemAvailable: 384 << 20,
		OOMKills:     -5, // negative untrusted value must clamp to 0
	})
	ev, err := ParseEvent(raw)
	if err != nil {
		t.Fatalf("ParseEvent(mem): %v", err)
	}
	if ev.OOMKills != 0 || ev.MemUsed != 128<<20 || ev.MemTotal != 512<<20 {
		t.Fatalf("sanitized mem event = %+v", ev)
	}

	s := &Supervisor{
		id: "sess1",
		m:  &host.Manifest{ID: "sess1"},
	}
	s.applyGuestEvent(ev)
	if s.m.Guest == nil || s.m.Guest.Mem == nil {
		t.Fatal("expected Manifest.Guest.Mem to be recorded")
	}
	if s.m.Guest.Mem.Used != 128<<20 || s.m.Guest.Mem.Total != 512<<20 || s.m.Guest.Mem.Available != 384<<20 {
		t.Fatalf("Guest.Mem = %+v", s.m.Guest.Mem)
	}
}

func TestSupervisorCHVUsesSyntheticDNSAndProxy(t *testing.T) {
	var gotArgs egress.StartArgs
	s := &Supervisor{
		id: "vm1",
		m: &host.Manifest{
			ID:    "vm1",
			Name:  "vm-session",
			Agent: host.AgentInfo{Kind: "claude"},
			Wall: &host.WallRecord{
				Level:  "microvm",
				Driver: "chv",
			},
		},
		opt: Options{
			EgressStarter: func(_ context.Context, _ string, args egress.StartArgs, _ egress.AuditSink) (*egress.Client, func(), error) {
				gotArgs = args
				return nil, func() {}, nil
			},
		},
	}
	if err := s.initEgress(); err != nil {
		t.Fatalf("initEgress: %v", err)
	}
	if gotArgs.DNSMode != egress.DNSModeSynthetic {
		t.Errorf("chv egress DNSMode = %q, want %q", gotArgs.DNSMode, egress.DNSModeSynthetic)
	}
	req := s.buildStartReq(nil, nil)
	if req.ProxyURL != process.GuestProxyURL {
		t.Errorf("chv StartReq.ProxyURL = %q, want %q", req.ProxyURL, process.GuestProxyURL)
	}
}

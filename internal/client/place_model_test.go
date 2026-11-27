package client

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestPlacementPoolUsesSavedTierAndLocalFeaturesBeforeFacts(t *testing.T) {
	for _, test := range []struct {
		name string
		host Host
		tier Tier
		need PlacementNeed
		fits bool
	}{
		{"own", Host{Name: "box"}, TierOwn, PlacementNeed{}, true},
		{"shared default", Host{Name: "box"}, TierShared, PlacementNeed{}, false},
		{"shared named tier", Host{Name: "box"}, TierShared, PlacementNeed{Tier: TierShared}, true},
		{"throwaway default", Host{Name: "box"}, TierThrowaway, PlacementNeed{}, false},
		{"throwaway untrusted", Host{Name: "box"}, TierThrowaway, PlacementNeed{Untrusted: true}, true},
		{"explicitly excluded", Host{Name: "box"}.WithPool(false), TierOwn, PlacementNeed{}, false},
		{"mandatory absent", Host{Name: "box", Place: HostPlacement{Features: []string{"gpu"}, Mandatory: []string{"gpu"}}}, TierOwn, PlacementNeed{}, false},
		{"mandatory named", Host{Name: "box", Place: HostPlacement{Features: []string{"gpu"}, Mandatory: []string{"gpu"}}}, TierOwn, PlacementNeed{Features: []string{"gpu"}}, true},
		{"feature missing", Host{Name: "box"}, TierOwn, PlacementNeed{Features: []string{"gpu"}}, false},
		{"own secret on shared", Host{Name: "box"}.WithPool(true), TierShared, PlacementNeed{RequiresOwn: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reason, err := poolPlacementReason(test.host, test.tier, test.need)
			if err != nil || (reason == "") != test.fits {
				t.Fatal("pool eligibility differs", reason, err)
			}
		})
	}
}

func placementCandidateFixture(t *testing.T, hostChange func(*Host), factsChange func(*api.HostFacts)) *autoPlacementCandidate {
	t.Helper()
	h := h5GrantState(t)
	if hostChange != nil {
		hostChange(&h)
		if err := SaveHosts(t.Context(), []Host{h}); err != nil {
			t.Fatal(err)
		}
	}
	facts := hostFactsFixtureValue()
	facts.Level = "microvm"
	facts.Walls = []api.WallProbe{{Driver: "microvm", Level: "microvm", CanPrepare: true}}
	if factsChange != nil {
		factsChange(&facts)
	}
	c := finishGeneratedTransportFixture(t, h, facts.HostID, "facts")
	c.pending = map[uint64]chan proto.Response{}
	sharedGrantWire(t, c, func(request proto.Request) any {
		if request.Op != "host.facts" {
			t.Fatal("selection fixture entered an application operation")
		}
		return facts
	})
	observed, err := c.readHostFacts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return &autoPlacementCandidate{host: h, tier: TierOwn, observation: observed,
		need: PlacementNeed{MinLevel: "microvm", Memory: 2 << 30, Disk: 1 << 30}}
}

func TestPlacementFactsRequireKnownExactRoomAndKeepVMReserve(t *testing.T) {
	for _, mode := range []string{"fits", "unknown room", "negative room", "inflight", "microvm-only health on gvisor", "prepared drift", "count limit"} {
		t.Run(mode, func(t *testing.T) {
			candidate := placementCandidateFixture(t, func(h *Host) {
				if mode == "prepared drift" {
					h.Prepared = &PreparedHost{Arch: "arm64", MemTotal: 8 << 30, Level: "microvm"}
				}
				if mode == "count limit" {
					h.Place.MaxSandboxes = 1
				}
			}, func(f *api.HostFacts) {
				switch mode {
				case "unknown room":
					f.AccountingKnown = false
				case "negative room":
					f.MemReserved, f.Room = 7<<30, -1<<30
				case "microvm-only health on gvisor":
					f.Walls = []api.WallProbe{{Driver: "gvisor", Level: "gvisor", CanPrepare: true}}
					f.Health = []api.Health{{ID: "microvm-only", Severity: "blocks_new", Levels: []string{"microvm"}}}
				case "count limit":
					f.Sandboxes = 1
				}
			})
			if mode == "microvm-only health on gvisor" {
				candidate.need.MinLevel = "gvisor"
			}
			inflight := int64(0)
			if mode == "inflight" {
				inflight = 3 << 30
			}
			reason, err := filterPlacementFacts(t.Context(), candidate, inflight, false, true)
			want := mode == "fits" || mode == "microvm-only health on gvisor"
			if err != nil || (reason == "") != want {
				t.Fatal("current native facts eligibility differs", mode, reason, err)
			}
			if mode == "fits" {
				budget, err := vmneed.BudgetFor(candidate.need.Memory)
				if err != nil || candidate.declared != budget.Limit || candidate.declared <= candidate.need.Memory {
					t.Fatal("selection omitted the exact VM unit reserve", err)
				}
			}
		})
	}
}

func TestPlacementRankKeepsWakePenaltyPreferenceAndStableStrategyOrder(t *testing.T) {
	makeCandidate := func(name string, room int64) *autoPlacementCandidate {
		return &autoPlacementCandidate{host: Host{Name: name}, room: room, observation: &hostFactsObservation{facts: api.HostFacts{}}}
	}
	box, other, paid := makeCandidate("box", 4), makeCandidate("other", 8), makeCandidate("paid", 16)
	box.observation.facts.PressureMemKnown, box.observation.facts.PressureMem = true, 10
	paid.wakeClass = 2
	list := []*autoPlacementCandidate{paid, box, other}
	settings := PlacementSettings{Strategy: "spread", Prefer: []string{"box"}}
	for range 2 {
		rankPlacementCandidates(list, settings, nil)
		if list[0] != other || list[1] != box || list[2] != paid || box.penalty != 1 {
			t.Fatal("penalty moved after preference or accumulated across rerank")
		}
	}
	box.observation.facts.PressureMem = 0
	settings.Prefer = nil
	rankPlacementCandidates(list, PlacementSettings{Strategy: "pack"}, nil)
	if list[0] != box {
		t.Fatal("pack did not choose the least eligible room")
	}
	rankPlacementCandidates(list, PlacementSettings{Strategy: "spread"}, []string{"other", "other", "other", "box"})
	if list[0] != box || other.penalty != 1 {
		t.Fatal("recent majority did not rank after its awake peers")
	}
}

func TestPlacementConfigRoundTripAndNeedGrammarUseActualDTOs(t *testing.T) {
	h := h5GrantState(t).WithPool(false)
	h.Place.Features, h.Place.Mandatory = []string{"gpu"}, []string{"gpu"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadHosts()
	if err != nil || len(loaded) != 1 {
		t.Fatal(err)
	}
	in, set := loaded[0].Pool()
	if !set || in || !slices.Equal(loaded[0].Place.Features, h.Place.Features) {
		t.Fatal("the actual hosts DTO dropped placement metadata")
	}
	clone := cloneGrantHost(loaded[0])
	clone.Place.Features[0], *clone.Place.Pool = "changed", true
	if loaded[0].Place.Features[0] != "gpu" || *loaded[0].Place.Pool {
		t.Fatal("the original saved host borrowed mutable placement data")
	}
	need, err := ParsePlacementNeed([]string{"arch=arm64,gpu", "tier=shared", "gpu"})
	if err != nil || need.Arch != "arm64" || need.Tier != TierShared || !slices.Equal(need.Features, []string{"gpu"}) {
		t.Fatal("need did not use the canonical tier/feature grammar", err)
	}
	for _, value := range []string{"arch=wrong", "tier=invalid", "unknown=value", "gpu,,cpu"} {
		if _, err := ParsePlacementNeed([]string{value}); err == nil {
			t.Fatal("invalid need was accepted", strings.TrimSpace(value))
		}
	}
}

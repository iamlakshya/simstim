//go:build linux

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestRestartVMSizeRequiresFreshNeedAndCountsGuestPlusReserve(t *testing.T) {
	need := resumeImageMetadataFixtureV1()
	beforeRaw, err := vmneed.EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	oldBudget, _ := vmneed.BudgetFor(need.GuestRAM)
	current := memoryReservationFixtureV1("chv", time.Now().UTC())
	current.VMNeedV1, current.RuntimeManifest = beforeRaw, &need.Runtime
	current.Limits = Limits{MemoryMax: oldBudget.Limit, Declared: oldBudget.Limit}
	if _, err := restartLimitsForRequestV1(&current, &current, 2<<30, false); err == nil {
		t.Fatal("a numeric request rewrote protected guest ownership")
	}
	fresh := need.Clone()
	fresh.GuestRAM = 2 << 30
	freshRaw, err := vmneed.EncodeV1(fresh, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	target := CloneManifest(current)
	target.VMNeedV1 = freshRaw
	got, err := restartLimitsForRequestV1(&current, &target, 2<<30, false)
	budget, _ := vmneed.BudgetFor(fresh.GuestRAM)
	if err != nil || got.MemoryMax != budget.Limit || got.Declared != budget.Limit || got.MemoryAsked != fresh.GuestRAM {
		t.Fatal("the selected guest paid its VMM reserve", got, budget, err)
	}
	memory := "2G"
	target.Policy = &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Memory: &memory}}}
	for _, request := range []int64{0, api.MemoryAuto} {
		got, err := restartLimitsForRequestV1(&current, &target, request, false)
		if err != nil || got.MemoryMax != budget.Limit {
			t.Fatal("policy/auto VM size bypassed the canonical budget", request, got, err)
		}
	}
	if !bytes.Equal(current.VMNeedV1, beforeRaw) || current.Limits.MemoryMax != oldBudget.Limit {
		t.Fatal("size preview changed the protected original")
	}
}

func TestResumeFreshNeedDataCannotCreateOriginalSelection(t *testing.T) {
	if _, err := resumeVMSelectionForArgsV1(t.Context(), &SessionOperation{}, api.ResumeArgs{VMNeedV1: json.RawMessage(`{"version":1}`)}); err == nil {
		t.Fatal("raw Need data issued a resume selection")
	}
	for _, operation := range []*SessionOperation{nil, {}, {id: "abc23def"}} {
		if checkAdmittedRestartCurrentV1(operation, &Manifest{}) == nil {
			t.Fatal("a copied operation issued a current selection")
		}
		if checkAdmittedRestartSelectionV1(operation, &Manifest{}, &Manifest{}) == nil {
			t.Fatal("manifest data issued a transition")
		}
	}
}

func TestRestartProcessAndGvisorSizesStayExactUnitCaps(t *testing.T) {
	for _, driver := range []string{"process", "gvisor"} {
		current := memoryReservationFixtureV1(driver, time.Now().UTC())
		got, err := restartLimitsForRequestV1(&current, &current, 2<<30, false)
		if err != nil || got.MemoryMax != 2<<30 || got.Declared != 2<<30 {
			t.Fatal("a non-VM cap acquired a VM reserve", driver, got, err)
		}
		_, err = restartLimitsForRequestV1(&current, &current, (256<<20)+1, false)
		var refused *proto.Error
		if !errors.As(err, &refused) || refused.Code != "memory_max" {
			t.Fatal("a non-VM invalid cap lost its original refusal", driver, err)
		}

	}
}

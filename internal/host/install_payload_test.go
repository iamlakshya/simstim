package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

func installHeaderFixtureV1(t *testing.T, plan api.InstallPlanV1) []byte {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(fmt.Sprintf("%08x\n", len(raw))), raw...)
}

func TestInstallPlanRejectsRoleEscapeAndPrematureHelper(t *testing.T) {
	base := api.InstallPlanV1{Version: 1, Nonce: strings.Repeat("a", 32), HelperVersion: "test-version", Arch: runtime.GOARCH, Items: []api.InstallItemV1{{Kind: supply.KindHelper, Name: brand.HostBinary, Size: 10, SHA256: strings.Repeat("b", 64)}}}
	if _, err := readInstallPlanV1(bytes.NewReader(installHeaderFixtureV1(t, base))); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*api.InstallPlanV1){
		func(p *api.InstallPlanV1) { p.HelperVersion = "../other" },
		func(p *api.InstallPlanV1) { p.Items[0].Name = "other-program" },
		func(p *api.InstallPlanV1) { p.Items[0].Size = 1 << 62 },
		func(p *api.InstallPlanV1) {
			p.Items = append(p.Items, api.InstallItemV1{Kind: supply.KindVT, Name: brand.VTBinary, Size: 1, SHA256: strings.Repeat("c", 64)})
		},
		func(p *api.InstallPlanV1) { p.Items = append(p.Items, p.Items[0]) },
	} {
		plan := base
		plan.Items = append([]api.InstallItemV1(nil), base.Items...)
		change(&plan)
		if _, err := readInstallPlanV1(bytes.NewReader(installHeaderFixtureV1(t, plan))); err == nil {
			t.Fatal("invalid data reached the native installer", plan)
		}
	}
}

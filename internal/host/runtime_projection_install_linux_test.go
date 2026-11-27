//go:build linux

package host

import (
	"encoding/json"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestRuntimeOriginActualBridgeInstallCommitToNewWriterV1(t *testing.T) {
	setupIsolatedPrepareHost(t)
	full, payloads := runtimeOriginFullFixtureV1(t)
	defaults := []supply.Item{}
	b, prior, call := runtimeOriginBridgeFixtureV1(t, full, &defaults, true)
	if prior != nil {
		t.Fatal("missing store manufactured original producer")
	}
	for _, item := range full.Items {
		response := call("runtime.install", runtimecache.RuntimeInstall{Action: "begin", Manifest: &full, SHA256: item.SHA256})
		if !response.OK {
			t.Fatal("real bridge begin refused", response.Error)
		}
		var part runtimecache.RuntimePart
		if json.Unmarshal(response.Result, &part) != nil || part.Part == "" {
			t.Fatal("real bridge did not issue original part")
		}
		response = call("runtime.install", runtimecache.RuntimeInstall{Action: "write", Part: part.Part, Data: payloads[item.SHA256]})
		if !response.OK {
			t.Fatal("real bridge original part write refused", response.Error)
		}
		response = call("runtime.install", runtimecache.RuntimeInstall{Action: "finish", Part: part.Part})
		if !response.OK {
			t.Fatal("real bridge original part finish refused", response.Error)
		}
	}
	committed := call("runtime.install", runtimecache.RuntimeInstall{Action: "commit", Manifest: &full, ImageDefaults: &defaults})
	if !committed.OK {
		t.Fatal("real bridge original commit refused", committed.Error)
	}
	original, err := b.runtime.selectedRuntime(b.ctx, full)
	if err != nil {
		t.Fatal("commit failed to retain original producer", err)
	}
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		selected, err := vmneed.SelectRuntimeForDriver(driver, full, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		receipt, use, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, driver, selected, runtimeOriginRawFixtureV1(t, full))
		if err != nil {
			t.Fatal(driver, err)
		}
		if receipt != original || use.original != original {
			t.Fatal("install projection fabricated a new producer")
		}
	}
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	operation, _, manifest := runtimeOriginNewWriterFixtureV1(t, b, full, "chv", agents)
	if err := saveReservedManifest(operation, manifest); err != nil {
		t.Fatal("actual committed producer failed New writer", err)
	}
	stored, err := operation.LoadContext(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RuntimeManifest == nil || len(stored.RuntimeManifest.Items) != 3 || len(original.files) != len(full.Items) {
		t.Fatal("committed producer was replaced by projection")
	}
}

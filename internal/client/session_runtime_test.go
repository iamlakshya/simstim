package client

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestSessionRuntimeHealthComparesSameArchitectureAndCurrentAdvisories(t *testing.T) {
	current := supply.Item{Kind: supply.KindVMM, Name: "cloud-hypervisor", Arch: "amd64", Version: "54.1", SHA256: strings.Repeat("a", 64)}
	other := current
	other.Arch, other.SHA256 = "arm64", strings.Repeat("b", 64)
	release := supply.Manifest{Items: []supply.Item{current, other}, Advisories: []supply.Advisory{{ID: "test-advisory", Kind: supply.AdvisoryVMM, Name: current.Name, Before: "54.1"}}}
	session := Session{Runtime: &api.SessionRuntimeInfoV1{Version: 1, Items: []api.SessionRuntimeItemV1{{Kind: current.Kind, Name: current.Name, Arch: current.Arch, Version: current.Version, SHA256: current.SHA256}}}}
	if got := sessionRuntimeHealthV1(session, release); got.Outdated || got.Exposed {
		t.Fatal("another architecture made the exact current runtime outdated", got)
	}
	session.Runtime.Items[0].Version, session.Runtime.Items[0].SHA256 = "54.0", strings.Repeat("c", 64)
	if got := sessionRuntimeHealthV1(session, release); !got.Outdated || !got.Exposed || got.Hint == "" {
		t.Fatal("current advisory did not cover the older running runtime", got)
	}
	session.Runtime.Version = 2
	if got := sessionRuntimeHealthV1(session, release); got.Outdated || got.Exposed {
		t.Fatal("future running metadata was selected", got)
	}
}

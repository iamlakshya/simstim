package client

import "testing"

func TestCanonicalHostClonesOwnMachineSource(t *testing.T) {
	for name, clone := range map[string]func(Host) Host{"grant": cloneGrantHost, "forget": cloneForgetHost, "observation": cloneObservationHost} {
		t.Run(name, func(t *testing.T) {
			h := Host{Name: "box", Machine: &Machine{Provider: "docker", ID: "resource", Source: &MachineSourceProvenance{
				Version: MachineSourceVersion, Kind: "docker-engine", ResourceID: "original-resource", Endpoint: "unix:///original/socket", EngineID: "original-engine",
				Config: MachineConfigSource{Role: MachineConfigAbsent, Path: "/original/config", ParentID: "original-parent"}}}}
			q := clone(h)
			if q.Machine == h.Machine || q.Machine.Source == h.Machine.Source {
				t.Fatal("canonical host snapshot borrowed mutable machine/source pointer")
			}
			q.Machine.Source.ResourceID = "changed-resource"
			q.Machine.Source.Endpoint = "unix:///changed/socket"
			q.Machine.Source.Config.ParentID = "changed-parent"
			if h.Machine.Source.ResourceID != "original-resource" || h.Machine.Source.Endpoint != "unix:///original/socket" || h.Machine.Source.Config.ParentID != "original-parent" {
				t.Fatal("snapshot mutation changed original provenance")
			}
			h.Machine.Source.Config.Path = "/later/config"
			if q.Machine.Source.Config.Path != "/original/config" {
				t.Fatal("later caller mutation changed captured provenance")
			}
		})
	}
}

func TestCanonicalHostClonePreservesUnknownMachineSourceData(t *testing.T) {
	// Clone delegates to P114's sole data clone, without normalising a future
	// version into a known one or promoting any stored strings to authority.
	h := Host{Name: "box", Machine: &Machine{Source: &MachineSourceProvenance{Version: MachineSourceVersion + 1, Kind: "future-kind", ResourceID: "future-resource",
		Config: MachineConfigSource{Role: MachineConfigRole("future-role"), Path: "/future/config", ObjectID: "future-object", ParentID: "future-parent"}}}}
	q := cloneObservationHost(h)
	if !SameEntry(h, q) || q.Machine.Source == h.Machine.Source {
		t.Fatal("clone discarded or shared future provenance data")
	}
	q.Machine.Source.Config.ObjectID = "changed"
	if h.Machine.Source.Config.ObjectID != "future-object" || h.Machine.Source.Version != MachineSourceVersion+1 {
		t.Fatal("clone interpreted/normalised unknown provenance")
	}
}

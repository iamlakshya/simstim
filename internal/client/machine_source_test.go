package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineSourceClosedConfigRolesAndImmutableClone(t *testing.T) {
	directory := t.TempDir()
	for _, config := range []MachineConfigSource{
		{Role: MachineConfigExisting, Path: directory, ObjectID: "observed-object"},
		{Role: MachineConfigAbsent, Path: filepath.Join(directory, "selected-absent-config"), ParentID: "observed-parent"},
		{Role: MachineConfigUnused},
	} {
		source := &MachineSourceProvenance{Version: MachineSourceVersion, Kind: "docker-engine", ResourceID: strings.Repeat("a", 64),
			Endpoint: "unix:///observed/direct/endpoint", EngineID: "observed-engine", Config: config}
		copy, err := NormaliseMachineSource("docker", source)
		if err != nil || copy == source || *copy != *source {
			t.Fatalf("actual role shape changed: %+v %v", copy, err)
		}
		copy.ResourceID = "caller mutation"
		copy.Config.Path = "caller mutation"
		if source.ResourceID == copy.ResourceID || source.Config.Path == copy.Config.Path {
			t.Fatal("source clone aliases immutable original data")
		}
	}
	lima := &MachineSourceProvenance{Version: MachineSourceVersion, Kind: "lima-plain", ResourceID: "original-instance",
		Config: MachineConfigSource{Role: MachineConfigExisting, Path: directory, ObjectID: "observed-instance"}}
	if _, err := NormaliseMachineSource("lima", lima); err != nil {
		t.Fatal(err)
	}
}

func TestMachineSourceUnknownFutureAndMixedRolesStayNonAuthorising(t *testing.T) {
	base := MachineSourceProvenance{Version: MachineSourceVersion, Kind: "docker-engine", ResourceID: strings.Repeat("b", 64),
		Endpoint: "unix:///observed/direct/endpoint", EngineID: "observed-engine", Config: MachineConfigSource{Role: MachineConfigUnused}}
	if _, err := NormaliseMachineSource("docker", nil); err == nil {
		t.Fatal("legacy missing provenance became permission")
	}
	for _, change := range []func(*MachineSourceProvenance){
		func(s *MachineSourceProvenance) { s.Version = 0 },
		func(s *MachineSourceProvenance) { s.Version++ },
		func(s *MachineSourceProvenance) { s.ResourceID = "container-name" },
		func(s *MachineSourceProvenance) { s.EngineID = "" },
		func(s *MachineSourceProvenance) { s.Kind = "safe" },
		func(s *MachineSourceProvenance) { s.Config.Path = t.TempDir() },
		func(s *MachineSourceProvenance) { s.Config.Role = "future" },
		func(s *MachineSourceProvenance) {
			s.Config = MachineConfigSource{Role: MachineConfigAbsent, Path: t.TempDir(), ObjectID: "wrong-role"}
		},
		func(s *MachineSourceProvenance) { s.Endpoint = "tcp://127.0.0.1:1234" },
		func(s *MachineSourceProvenance) { s.Endpoint = "ssh://name:password@example.invalid" },
		func(s *MachineSourceProvenance) { s.Endpoint = "unix:///observed/endpoint?credential=not-a-value" },
		func(s *MachineSourceProvenance) { s.Endpoint = "unix:///observed/endpoint#not-a-value" },
	} {
		source := base
		change(&source)
		if _, err := NormaliseMachineSource("docker", &source); err == nil {
			t.Fatalf("unknown or mixed source became permission: %+v", source)
		}
	}
	if _, err := NormaliseMachineSource("lima", &base); err == nil {
		t.Fatal("a different provider accepted Docker provenance")
	}
}

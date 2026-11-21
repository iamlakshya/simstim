package gvisor

import (
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTopologyDataCannotCarryPrivateProgramOrHostPath(t *testing.T) {
	valid := `{"v":2,"id":"gvisorprobe","role":"probe-true","mounts":[{"role":"mount","source":0,"guest":"/work","write":true,"identity":"native-object"}]}`
	if _, err := checkNativeTopologyWireV1([]byte(valid), 1); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		strings.Replace(valid, `"mounts":`, `"argv":["untrusted"],"mounts":`, 1),
		strings.Replace(valid, `"mounts":`, `"env":{"SECRET":"value"},"mounts":`, 1),
		strings.Replace(valid, `"probe-true"`, `"agent-command"`, 1),
		strings.Replace(valid, `"v":2`, `"v":3`, 1),
		strings.Replace(valid, `"source":0`, `"source":0,"host":"/unrelated"`, 1),
		strings.Replace(valid, `"source":0`, `"source":1`, 1),
		strings.Replace(valid, `"/work"`, `"/proc/1"`, 1),
	}
	for _, raw := range bad {
		if _, err := checkNativeTopologyWireV1([]byte(raw), 1); err == nil {
			t.Fatal("an unsupported setup/private field became immutable topology", raw)
		}
	}
}

func TestKeptStateRoleStaysOnOriginalSessionObject(t *testing.T) {
	session := t.TempDir()
	kept := filepath.Join(session, "state", ".config")
	if err := os.MkdirAll(kept, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(kept)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := files.Identity(file)
	if err != nil {
		t.Fatal(err)
	}
	spec := wall.Spec{ID: "aaaa2222", SessionDir: session, Root: wall.Reset, StateDirs: []string{".config"}}
	mount := wall.Mount{Host: kept, Guest: "/home/agent/.config", Write: true, Identity: identity}
	role, err := nativeTopologyMountRoleV2(spec, mount)
	if err != nil || role != "kept-dir" {
		t.Fatal("approved kept state refused", role, err)
	}
	raw := `{"v":2,"id":"aaaa2222","role":"guest-init","mounts":[{"role":"kept-dir","source":0,"guest":"/home/agent/.config","write":true,"identity":"native-object"}]}`
	if _, err := checkNativeTopologyWireV1([]byte(raw), 1); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(raw, `"kept-dir"`, `"mount"`, 1),
		strings.Replace(raw, `"/home/agent/.config"`, `"/home/user/.config"`, 1),
		strings.Replace(raw, `"/home/agent/.config"`, `"/home/agent/../other"`, 1),
		strings.Replace(raw, `"write":true`, `"write":false`, 1),
		strings.Replace(raw, `"write":true`, `"write":null`, 1),
		strings.Replace(raw, `"identity":"native-object"`, `"identity":"native-object","parse_state":true`, 1),
	} {
		if _, err := checkNativeTopologyWireV1([]byte(bad), 1); err == nil {
			t.Fatal("unapproved home/escape/content parser became setup", bad)
		}
	}
	capsule, err := freezeNativeTopologyV1(spec, []wall.Mount{mount}, []*os.File{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer capsule.Close()
	if err := capsule.CheckSetupV1(spec, []wall.Mount{mount}); err != nil {
		t.Fatal(err)
	}
	projection, err := capsule.RetainPublicV1()
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	if projection.ID != spec.ID || projection.Role != "guest-init" || len(projection.Sources) != 1 {
		t.Fatal("the public projection changed its fixed original roles")
	}
	actual, err := projection.Sources[0].Stat()
	expected, expectedErr := file.Stat()
	if err != nil || expectedErr != nil || !os.SameFile(actual, expected) {
		t.Fatal("public setup substituted the original kept-state object", err, expectedErr)
	}
	if err := capsule.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capsule.CheckSetupV1(spec, []wall.Mount{mount}); err == nil {
		t.Fatal("the closed original capsule was renewed from its public projection")
	}
	if _, err := projection.Sources[0].Stat(); err != nil {
		t.Fatal("closing the original capsule discarded the separately owned public projection", err)
	}
	changed := mount
	changed.Host = filepath.Join(session, "foreign")
	if _, err := nativeTopologyMountRoleV2(spec, changed); err == nil {
		t.Fatal("new host object acquired kept-state role")
	}
	forged := mount
	forged.Identity = "foreign-native-object"
	if capsule, err := freezeNativeTopologyV1(spec, []wall.Mount{forged}, []*os.File{file}, false); err == nil {
		capsule.Close()
		t.Fatal("forged native source issued immutable setup")
	}
	escaped := wall.Spec{ID: spec.ID, SessionDir: session, Root: wall.Reset, StateDirs: []string{"../foreign"}}
	if capsule, err := freezeNativeTopologyV1(escaped, []wall.Mount{mount}, []*os.File{file}, false); err == nil {
		capsule.Close()
		t.Fatal("escaping state plan became setup")
	}
	if _, err := nativeTopologyMountRoleV2(wall.Spec{}, mount); err == nil {
		t.Fatal("home mount without original approved state became setup")
	}
}

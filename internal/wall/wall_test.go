package wall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLevelAndJSON(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Level
	}{
		{"none", None},
		{"process", Process},
		{"gvisor", GVisor},
		{"microvm", MicroVM},
		{" Process ", Process},
	} {
		got, err := ParseLevel(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseLevel(%q) = (%v, %v), want (%v, nil)", c.in, got, err, c.want)
		}
		if got.String() != c.want.String() {
			t.Errorf("String() = %q, want %q", got.String(), c.want.String())
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", got, err)
		}
		var back Level
		if err := json.Unmarshal(raw, &back); err != nil || back != got {
			t.Errorf("Unmarshal(%s) = (%v, %v), want %v", raw, back, err, got)
		}
	}
	if _, err := ParseLevel("invalid"); err == nil {
		t.Error("ParseLevel(invalid) succeeded")
	}
	var bad Level
	if err := json.Unmarshal([]byte(`"bogus"`), &bad); err == nil {
		t.Error("Unmarshal(bogus) succeeded")
	}
}

func TestBest(t *testing.T) {
	if got := Best(nil); got != None {
		t.Errorf("Best(nil) = %v, want none", got)
	}
	probes := []Probe{
		{Driver: "chv", Level: MicroVM, OK: false},
		{Driver: "process", Level: Process, OK: true},
		{Driver: "gvisor", Level: GVisor, OK: true},
	}
	if got := Best(probes); got != GVisor {
		t.Errorf("Best = %v, want gvisor", got)
	}
	probes[0].OK = true
	if got := Best(probes); got != MicroVM {
		t.Errorf("Best with microvm OK = %v, want microvm", got)
	}
}

type fakeDriver struct {
	name  string
	level Level
	ok    bool
}

func (f fakeDriver) Name() string { return f.name }
func (f fakeDriver) Level() Level { return f.level }
func (f fakeDriver) Probe(context.Context) Probe {
	return Probe{Driver: f.name, Level: f.level, OK: f.ok}
}
func (f fakeDriver) Prepare(context.Context, Spec) (Sandbox, error) { return nil, nil }
func (f fakeDriver) Open(context.Context, string) (Sandbox, error)  { return nil, nil }

func TestDriversOrderingAndProbeAll(t *testing.T) {
	driversMu.Lock()
	saved := drivers
	drivers = map[string]Driver{}
	driversMu.Unlock()
	t.Cleanup(func() {
		driversMu.Lock()
		drivers = saved
		driversMu.Unlock()
	})

	Register(fakeDriver{name: "process", level: Process, ok: true})
	Register(fakeDriver{name: "chv", level: MicroVM, ok: false})
	Register(fakeDriver{name: "gvisor", level: GVisor, ok: true})

	ds := Drivers()
	if len(ds) != 3 || ds[0].Name() != "chv" || ds[1].Name() != "gvisor" || ds[2].Name() != "process" {
		t.Fatalf("Drivers() = %+v, want [chv gvisor process]", ds)
	}
	probes := ProbeAll(context.Background())
	if Best(probes) != GVisor {
		t.Errorf("Best(ProbeAll) = %v, want gvisor", Best(probes))
	}
	apiProbes := ToAPIProbes(probes)
	if len(apiProbes) != 3 || apiProbes[0].Level != "microvm" || apiProbes[1].Level != "gvisor" {
		t.Errorf("ToAPIProbes = %+v", apiProbes)
	}
}

func TestProbeCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "walls.json")
	if _, ok := ReadCache(path, "boot-1", "1.0.0"); ok {
		t.Fatal("ReadCache hit on missing file")
	}

	want := []Probe{{Driver: "process", Level: Process, OK: true, Version: "0.11.0", Facts: map[string]string{"landlock_abi": "6"}}}
	if err := WriteCache(path, "boot-1", "1.0.0", want); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, ok := ReadCache(path, "boot-1", "1.0.0")
	if !ok || len(got) != 1 || !got[0].OK || got[0].Version != "0.11.0" {
		t.Fatalf("ReadCache = (%+v, %v), want %+v", got, ok, want)
	}
	if _, ok := ReadCache(path, "boot-2", "1.0.0"); ok {
		t.Error("ReadCache hit on different boot ID")
	}
	if _, ok := ReadCache(path, "boot-1", "1.0.1"); ok {
		t.Error("ReadCache hit on different helper version")
	}
	if _, ok := ReadCache(path, "boot-1", ""); ok {
		t.Error("ReadCache hit on empty helper version")
	}
	if err := os.WriteFile(path, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCache(path, "boot-1", "1.0.0"); ok {
		t.Error("ReadCache hit on corrupt JSON")
	}
}

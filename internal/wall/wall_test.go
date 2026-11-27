package wall

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func isolate(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(root, key))
	}
}

func TestParseLevel(t *testing.T) {
	isolate(t)
	for _, tt := range []struct {
		text  string
		level Level
	}{{"none", None}, {"process", Process}, {"gvisor", GVisor}, {"microvm", MicroVM}} {
		got, err := ParseLevel(tt.text)
		if err != nil || got != tt.level || got.String() != tt.text {
			t.Errorf("ParseLevel(%q) = %v, %v", tt.text, got, err)
		}
	}
	for _, text := range []string{"", "auto", "PROCESS", " process", "microvm ", "vz", "unknown"} {
		if _, err := ParseLevel(text); err == nil {
			t.Errorf("ParseLevel(%q) passed", text)
		}
	}
	if Level(-1).String() != "unknown" || Level(4).String() != "unknown" {
		t.Fatal("invalid level has a supported name")
	}
}

func TestBest(t *testing.T) {
	isolate(t)
	for _, tt := range []struct {
		name   string
		probes []Probe
		want   Level
	}{
		{"empty", nil, None},
		{"failed strong", []Probe{{Level: MicroVM}, {Level: Process, OK: true}}, Process},
		{"unordered", []Probe{{Level: Process, OK: true}, {Level: MicroVM, OK: true}, {Level: GVisor, OK: true}}, MicroVM},
		{"invalid levels", []Probe{{Level: Level(-1), OK: true}, {Level: Level(4), OK: true}}, None},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Best(tt.probes); got != tt.want {
				t.Errorf("Best = %v, want %v", got, tt.want)
			}
		})
	}
}

type testDriver struct {
	name  string
	level Level
}

func (d testDriver) Name() string { return d.name }
func (d testDriver) Level() Level { return d.level }
func (d testDriver) Probe(context.Context) Probe {
	return Probe{Driver: d.name, Level: d.level}
}
func (testDriver) Prepare(context.Context, Spec) (Sandbox, error) {
	return nil, fmt.Errorf("test driver has no sandbox")
}
func (testDriver) Open(context.Context, string) (Sandbox, error) {
	return nil, fmt.Errorf("test driver has no sandbox")
}

func TestDriverRegistry(t *testing.T) {
	isolate(t)
	registry.Lock()
	saved := registry.drivers
	registry.drivers = nil
	registry.Unlock()
	t.Cleanup(func() {
		registry.Lock()
		registry.drivers = saved
		registry.Unlock()
	})
	for _, d := range []testDriver{{"process", Process}, {"chv", MicroVM}, {"nitro", MicroVM}, {"none", None}, {"gvisor", GVisor}} {
		if err := Register(d); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, d := range Drivers() {
		names = append(names, d.Name())
	}
	if !slices.Equal(names, []string{"nitro", "chv", "gvisor", "process", "none"}) {
		t.Errorf("driver order: %v", names)
	}
	copy := Drivers()
	copy[0] = testDriver{"changed", None}
	if Drivers()[0].Name() != "nitro" {
		t.Fatal("caller changed registered drivers through returned slice")
	}
	for _, d := range []Driver{nil, testDriver{"", Process}, testDriver{"bad name", Process}, testDriver{"x", Level(-1)}, testDriver{"x", Level(4)}, testDriver{"process", Process}} {
		if err := Register(d); err == nil {
			t.Errorf("Register(%v) passed", d)
		}
	}
}

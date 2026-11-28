//go:build linux

package host

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
)

type fakeBus struct {
	stopErr    error
	jobResult  string
	props      map[string][]string
	stopCalls  []string
	resetCalls []string
	closed     bool
}

func (f *fakeBus) StopUnitContext(_ context.Context, name, mode string, ch chan<- string) (int, error) {
	f.stopCalls = append(f.stopCalls, name+":"+mode)
	if f.stopErr != nil {
		return 0, f.stopErr
	}
	if ch != nil {
		res := f.jobResult
		if res == "" {
			res = "done"
		}
		ch <- res
	}
	return 1, nil
}

func (f *fakeBus) ResetFailedUnitContext(_ context.Context, name string) error {
	f.resetCalls = append(f.resetCalls, name)
	return nil
}

func (f *fakeBus) GetUnitPropertyContext(_ context.Context, _ string, prop string) (*dbus.Property, error) {
	vals := f.props[prop]
	if len(vals) == 0 {
		return nil, errors.New("org.freedesktop.systemd1.NoSuchUnit: Unit not loaded")
	}
	v := vals[0]
	if len(vals) > 1 {
		f.props[prop] = vals[1:]
	} else {
		delete(f.props, prop)
	}
	return &dbus.Property{Name: prop, Value: godbus.MakeVariant(v)}, nil
}

func (f *fakeBus) Close() { f.closed = true }

func withFakeBus(t *testing.T, b systemdBus, dialErr error) {
	t.Helper()
	old := dialSystemdBus
	dialSystemdBus = func(context.Context) (systemdBus, error) {
		if dialErr != nil {
			return nil, dialErr
		}
		return b, nil
	}
	t.Cleanup(func() { dialSystemdBus = old })
}

func TestStopUnitViaDBus(t *testing.T) {
	t.Run("stops cleanly and waits for unload", func(t *testing.T) {
		fb := &fakeBus{
			jobResult: "done",
			props: map[string][]string{
				"ActiveState": {"inactive"},
				"LoadState":   {"loaded", "not-found"},
			},
		}
		withFakeBus(t, fb, nil)
		if !stopUnitViaDBus(context.Background(), "s-abc.service") {
			t.Fatal("stopUnitViaDBus returned false")
		}
		if !fb.closed {
			t.Error("bus connection was not closed")
		}
		if !slices.Equal(fb.stopCalls, []string{"s-abc.service:replace"}) {
			t.Errorf("stop calls = %q", fb.stopCalls)
		}
		if len(fb.resetCalls) != 0 {
			t.Errorf("unexpected reset-failed calls: %q", fb.resetCalls)
		}
	})

	t.Run("resets failed unit so --collect unloads it", func(t *testing.T) {
		fb := &fakeBus{
			jobResult: "done",
			props: map[string][]string{
				"ActiveState": {"failed"},
				"LoadState":   {"not-found"},
			},
		}
		withFakeBus(t, fb, nil)
		if !stopUnitViaDBus(context.Background(), "s-fail.service") {
			t.Fatal("stopUnitViaDBus returned false")
		}
		if !slices.Equal(fb.resetCalls, []string{"s-fail.service"}) {
			t.Errorf("reset-failed calls = %q, want [s-fail.service]", fb.resetCalls)
		}
	})

	t.Run("treats NoSuchUnit as already stopped", func(t *testing.T) {
		fb := &fakeBus{stopErr: errors.New("org.freedesktop.systemd1.NoSuchUnit: Unit s-gone.service not loaded")}
		withFakeBus(t, fb, nil)
		if !stopUnitViaDBus(context.Background(), "s-gone.service") {
			t.Fatal("NoSuchUnit should count as handled")
		}
	})

	t.Run("falls back to systemctl when D-Bus is unavailable", func(t *testing.T) {
		withFakeBus(t, nil, errors.New("no session bus"))
		if stopUnitViaDBus(context.Background(), "s-nobus.service") {
			t.Fatal("expected fallback when D-Bus cannot connect")
		}
	})
}

func TestStopUnitCLIFallbackResetsFailedLoadedUnit(t *testing.T) {
	var calls [][]string
	old := systemctl
	shows := 0
	systemctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 0 && args[0] == "show" {
			shows++
			if shows == 1 {
				return []byte("loaded\n"), nil
			}
			return []byte("not-found\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { systemctl = old })

	stopUnit("s-stuck.service")
	want := [][]string{
		{"stop", "s-stuck.service"},
		{"show", "s-stuck.service", "-p", "LoadState", "--value"},
		{"reset-failed", "s-stuck.service"},
		{"show", "s-stuck.service", "-p", "LoadState", "--value"},
	}
	if !slices.EqualFunc(calls, want, slices.Equal[[]string]) {
		t.Fatalf("systemctl calls = %q, want %q", calls, want)
	}
}

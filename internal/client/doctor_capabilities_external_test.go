package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func doctorCapabilityFixture(t *testing.T, sandbox bool) (*client.Conn, *clienttest.Transport, context.Context) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	transport := &clienttest.Transport{HostID: strings.Repeat("a", 32), Answers: map[string]any{"doctor": api.HostInfo{Hostname: "reported-host", Checks: []api.Check{}}}}
	if sandbox {
		transport.Features = []string{"sandbox"}
	}
	machine := clienttest.NewMachine(client.MachineRunning, transport)
	client.RegisterProvider("doctor-capability-fixture", func(client.Host) (client.Lifecycle, error) { return machine, nil })
	host := client.Host{Name: "box", Machine: &client.Machine{Provider: "doctor-capability-fixture", ID: "owned-fixture"}}
	if err := client.SaveHosts(t.Context(), []client.Host{host}); err != nil {
		t.Fatal(err)
	}
	host, err := client.FindHost(host.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, transport.HostID, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(client.WithoutInstall(t.Context()), 15*time.Second)
	connection, err := client.Dial(ctx, host, false)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); connection.Close() })
	return connection, transport, ctx
}

func TestDoctorCollectsTheActualFreshCapabilityReport(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "old helper", true: "sandbox helper"}[sandbox], func(t *testing.T) {
			connection, transport, ctx := doctorCapabilityFixture(t, sandbox)
			expected := api.CapProbe{OS: "linux", Arch: "arm64", Kernel: "6.12.0", MemTotal: 1024, Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true, Facts: map[string]string{"runtime_available": "true"}}}}
			transport.Answers["sandbox.probe"] = expected
			before := len(transport.Requests())
			report, err := connection.Doctor(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantOps := []string{"doctor"}
			if sandbox {
				wantOps = append(wantOps, "sandbox.probe")
			}
			if ops := transport.Requests()[before:]; !reflect.DeepEqual(ops, wantOps) {
				t.Fatalf("doctor did not use its real feature-gated RPC path: %q, want %q", ops, wantOps)
			}
			if report.Hostname != "reported-host" {
				t.Fatal("collecting capabilities discarded the ordinary report")
			}
			if sandbox {
				if report.Sandbox == nil || !reflect.DeepEqual(*report.Sandbox, expected) {
					t.Fatalf("fresh report was replaced/inferred: %+v", report.Sandbox)
				}
			} else if report.Sandbox != nil {
				t.Fatal("an older helper acquired invented runtime evidence")
			}
		})
	}
}

func TestDoctorProbeFailureIsVisibleAndAvailabilityStaysUnknown(t *testing.T) {
	connection, transport, ctx := doctorCapabilityFixture(t, true)
	transport.Fail("sandbox.probe", &proto.Error{Code: "probe_denied", Message: "probe denied\x1b[31m", Hint: "Check the runtime permissions."})
	report, err := connection.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sandbox != nil || len(report.Checks) != 1 || report.Checks[0].OK || report.Checks[0].Name != "sandbox capabilities" || report.Checks[0].Fix != "Check the runtime permissions." || strings.ContainsRune(report.Checks[0].Detail, '\x1b') {
		t.Fatalf("failed capabilities were hidden or became available: %+v", report)
	}
}

func TestDoctorProbeCancellationWinsAReadyReply(t *testing.T) {
	connection, transport, base := doctorCapabilityFixture(t, true)
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	transport.Answers["sandbox.probe"] = api.CapProbe{OS: "linux"}
	transport.OnRequest = func(operation string) {
		if operation == "sandbox.probe" {
			cancel()
		}
	}
	report, err := connection.Doctor(ctx)
	if !errors.Is(err, context.Canceled) || report.Sandbox != nil {
		t.Fatalf("a cancelled probe published fresh facts: %+v, %v", report.Sandbox, err)
	}
}

func TestCleanHostInfoOwnsTheCapabilityDisplayCopy(t *testing.T) {
	raw := client.HostInfo{Sandbox: &api.CapProbe{OS: "lin\x1b[31mux", Walls: []api.WallProbe{{Driver: "process", Level: "process", Reason: "reason\x07", Facts: map[string]string{"runtime_available": "true"}}}}}
	clean := client.CleanHostInfo(raw)
	if clean.Sandbox == nil || strings.ContainsRune(clean.Sandbox.OS, '\x1b') || strings.ContainsRune(clean.Sandbox.Walls[0].Reason, '\x07') {
		t.Fatalf("capability display retained terminal controls: %+v", clean.Sandbox)
	}
	clean.Sandbox.Walls[0].Facts["runtime_available"] = "false"
	if raw.Sandbox.OS != "lin\x1b[31mux" || raw.Sandbox.Walls[0].Reason != "reason\x07" || raw.Sandbox.Walls[0].Facts["runtime_available"] != "true" {
		t.Fatal("display cleanup mutated the raw report/policy evidence")
	}
}

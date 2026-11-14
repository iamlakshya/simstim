//go:build linux && chvintegration

package microvm_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func TestMain(m *testing.M) { hosttest.Main(m) }

// The operator supplies this fresh CLIENT plan and its verified cache only
// after explicit admission. No host inventory is promoted into these pins.
func admittedPlan(t testing.TB) wall.Spec {
	t.Helper()
	if os.Getenv("WALL_CHV_ADMITTED") != "1" {
		t.Skip("native KVM integration requires explicit paced admission")
	}
	root := os.Getenv("WALL_CHV_TEST_ROOT")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		t.Fatal("missing private test root")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		value := os.Getenv(key)
		if !strings.HasPrefix(value, root+string(filepath.Separator)) {
			t.Fatalf("%s is outside the admitted isolated root", key)
		}
	}
	file, err := os.Open(os.Getenv("WALL_CHV_CLIENT_PLAN"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	var spec wall.Spec
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("client fixture has trailing content")
	}
	spec.ID, err = api.RandomSessionID()
	if err != nil {
		t.Fatal(err)
	}
	spec.SessionDir = paths.SessionDir(spec.ID)
	if err := paths.EnsurePrivateDir(spec.SessionDir); err != nil {
		t.Fatal(err)
	}
	manifest := &host.Manifest{Schema: 1, ID: spec.ID, Name: spec.ID, CreatedAt: time.Now().UTC()}
	if err := manifest.Save(); err != nil {
		t.Fatal(err)
	}
	return spec
}

func bootNative(t testing.TB, ctx context.Context, spec wall.Spec, reopen bool) (wall.Sandbox, *link.Session) {
	t.Helper()
	factory := host.CloudHypervisorResourcesV1()
	expected := wall.WithExpectedSpec(ctx, spec)
	var sandbox wall.Sandbox
	var err error
	if reopen {
		sandbox, err = factory.Open(expected, spec.SessionDir)
	} else {
		sandbox, err = factory.Build(expected)
	}
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	conn, err := sandbox.Start(ctx)
	if err != nil {
		_ = sandbox.Destroy()
		t.Fatal(err)
	}
	if time.Since(started) > chv.BootTimeout {
		_ = sandbox.Destroy()
		t.Fatal("boot exceeded the whole start/ACK/hello budget")
	}
	return sandbox, link.New(conn, link.Supervisor, link.Options{})
}

func runControlledGuest(ctx context.Context, peer *link.Session, id, script string) (int, error) {
	ctl, err := peer.Open(ctx, link.KindCtl, nil)
	if err != nil {
		return -1, err
	}
	defer ctl.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = ctl.SetDeadline(deadline)
	}
	prepare, err := json.Marshal(proto.CtlVMPrepareArgs{Session: id, VM: proto.VMStartV1{Version: 1, System: "reset"}})
	if err != nil {
		return -1, err
	}
	if err := proto.NewWriter(ctl).WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: proto.CtlVMPrepare, Args: prepare}); err != nil {
		return -1, err
	}
	reader := bufio.NewReader(ctl)
	for {
		frame, err := proto.Read(reader)
		if err != nil {
			return -1, err
		}
		if frame.Type != proto.TResponse {
			continue
		}
		var response proto.Response
		if err := json.Unmarshal(frame.Payload, &response); err != nil {
			return -1, err
		}
		if response.ID != 2 {
			continue
		}
		var result proto.CtlVMPrepareResult
		if !response.OK || json.Unmarshal(response.Result, &result) != nil || result.GuestABI != proto.GuestOwnedPTYV2 {
			return -1, fmt.Errorf("controlled guest prepare did not apply actual v2: %v", response.Error)
		}
		break
	}
	args, err := json.Marshal(proto.CtlStartArgs{Session: id, RunToken: "native-owned-run", DNS: proto.DNSSynthetic, Agent: "shell", Argv: []string{"/bin/sh", "-ceu", script}, Env: []string{"HOME=/home/agent", "PATH=/usr/bin:/bin", "TERM=dumb", "LANG=C"}, Cwd: "/work", Cols: 80, Rows: 24})
	if err != nil {
		return -1, err
	}
	if err := proto.NewWriter(ctl).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: proto.CtlStart, Args: args}); err != nil {
		return -1, err
	}
	for {
		frame, err := proto.Read(reader)
		if err != nil {
			return -1, err
		}
		if frame.Type == proto.TResponse {
			var response proto.Response
			if err := json.Unmarshal(frame.Payload, &response); err != nil {
				return -1, err
			}
			if response.ID == 1 && !response.OK {
				return -1, fmt.Errorf("controlled guest start: %v", response.Error)
			}
		}
		if frame.Type == proto.TEvent {
			var event proto.CtlEvent
			if err := json.Unmarshal(frame.Payload, &event); err != nil {
				return -1, err
			}
			if event.Event == proto.EventEnded && event.Session == id && event.ExitCode != nil {
				return *event.ExitCode, nil
			}
		}
	}
}

func TestNativeBootSerialsActual512WriteAndPersistentRestart(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sandbox, peer := bootNative(t, ctx, spec, false)
	t.Cleanup(func() { _ = sandbox.Destroy() })
	// A preflight failure is distinct from an actual direct write failure.
	script := `test "$(cat /sys/block/vda/serial)" = tools
test "$(cat /sys/block/vdb/serial)" = image
test "$(cat /sys/block/vdc/serial)" = agents
test "$(cat /sys/block/vdd/serial)" = session
command -v dd >/dev/null
test -w /work
printf persistent > /work/chv-persistence
if dd if=/dev/zero of=/work/direct512 bs=512 count=1 oflag=direct conv=fsync; then exit 0; else exit 77; fi`
	code, err := runControlledGuest(ctx, peer, spec.ID, script)
	peer.Close()
	if err != nil || code != 0 && code != 77 {
		t.Fatalf("controlled guest check: %d %v", code, err)
	}
	if err := chv.RecordDirectResult(ctx, spec, code == 0); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Stop(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	sandbox.Wait()
	second, peer := bootNative(t, ctx, spec, true)
	t.Cleanup(func() { _ = second.Destroy() })
	code, err = runControlledGuest(ctx, peer, spec.ID, `test "$(cat /work/chv-persistence)" = persistent`)
	peer.Close()
	if err != nil || code != 0 {
		t.Fatalf("existing session disk was not reused: %d %v", code, err)
	}
	_ = second.Stop(ctx, 5*time.Second)
	second.Wait()
}

func TestNativeWrongClientVerityRootRefusesBeforeHello(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if spec.VM == nil {
		t.Fatal("fixture lacks fresh VM authority")
	}
	spec.VM.Image.RootHash = strings.Repeat("1", 64)
	sandbox, err := host.CloudHypervisorResourcesV1().Prepare(wall.WithExpectedSpec(ctx, spec))
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Destroy()
	if conn, err := sandbox.Start(ctx); err == nil {
		conn.Close()
		t.Fatal("wrong client verity root reached a successful hello")
	}
	sandbox.Wait()
}

func TestNativeStartupCancellationIsReleasedButOwnerCancellationStopsVM(t *testing.T) {
	spec := admittedPlan(t)
	owner, cancelOwner := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelOwner()
	sandbox, err := host.CloudHypervisorResourcesV1().Build(wall.WithExpectedSpec(owner, spec))
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Destroy()
	startup, cancelStartup := context.WithTimeout(owner, chv.BootTimeout)
	conn, err := sandbox.Start(startup)
	if err != nil {
		cancelStartup()
		t.Fatal(err)
	}
	defer conn.Close()
	cancelStartup()
	ended := make(chan wall.Exit, 1)
	go func() { ended <- sandbox.Wait() }()
	select {
	case exit := <-ended:
		t.Fatalf("startup context killed the healthy VM: %+v", exit)
	case <-time.After(100 * time.Millisecond):
	}
	state, ok := sandbox.(interface {
		State(context.Context) ([]byte, error)
	})
	if !ok {
		t.Fatal("actual driver has no bounded VMM state operation")
	}
	if _, err := state.State(owner); err != nil {
		t.Fatalf("healthy VM did not outlive startup cancellation: %v", err)
	}
	cancelOwner()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("owner cancellation did not reap the complete owned jail")
	}
}

func BenchmarkNativeCloudHypervisorBootAndOwnedStop(b *testing.B) {
	for range b.N {
		spec := admittedPlan(b)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		sandbox, peer := bootNative(b, ctx, spec, false)
		peer.Close()
		if err := sandbox.Kill(); err != nil {
			b.Fatal(err)
		}
		sandbox.Wait()
		if err := sandbox.Destroy(); err != nil {
			b.Fatal(err)
		}
		cancel()
	}
}

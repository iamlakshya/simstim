package host_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func isolateHostState(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	return root
}

func TestHostIDCreatesOnceWithPrivateMode(t *testing.T) {
	isolateHostState(t)

	id1, err := host.HostID()
	if err != nil {
		t.Fatalf("first HostID: %v", err)
	}
	if !host.ValidHostID(id1) {
		t.Fatalf("HostID = %q, want 32 lower-case hex characters", id1)
	}

	id2, err := host.HostID()
	if err != nil {
		t.Fatalf("second HostID: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("HostID changed from %q to %q", id1, id2)
	}

	if runtime.GOOS != "windows" {
		st, err := os.Stat(paths.HostID())
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != 0o600 {
			t.Fatalf("host-id mode = %o, want 0600", got)
		}
	}
}

func TestHostIDConcurrentFirstRunsGiveOneID(t *testing.T) {
	for round := range 10 {
		isolateHostState(t)

		const n = 16
		start := make(chan struct{})
		ids := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go func(i int) {
				defer wg.Done()
				<-start
				ids[i], errs[i] = host.HostID()
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d worker %d: %v", round, i, err)
			}
		}
		want := ids[0]
		if !host.ValidHostID(want) {
			t.Fatalf("round %d: invalid ID %q", round, want)
		}
		for i, got := range ids {
			if got != want {
				t.Fatalf("round %d: worker %d got %q, worker 0 got %q", round, i, got, want)
			}
		}
		entries, err := os.ReadDir(paths.StateDir())
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "host-id" {
			t.Fatalf("round %d: unexpected entries in state dir: %v", round, entries)
		}
	}
}

func TestHostIDInvalidFileFailsAndStaysUnchanged(t *testing.T) {
	for _, bad := range []string{
		"",
		"\n",
		"0123456789abcdef0123456789abcde\n",
		"0123456789abcdef0123456789abcdef0\n",
		"0123456789ABCDEF0123456789abcdef\n",
		"0123456789abcdef0123456789abcdeg\n",
	} {
		isolateHostState(t)
		if err := paths.EnsurePrivateDir(paths.StateDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.HostID(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if id, err := host.HostID(); err == nil {
			t.Fatalf("bad=%q: got %q, want error", bad, id)
		}
		after, err := os.ReadFile(paths.HostID())
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != bad {
			t.Fatalf("bad=%q: helper replaced invalid host-id with %q", bad, string(after))
		}
	}

	if runtime.GOOS != "windows" {
		root := isolateHostState(t)
		if err := paths.EnsurePrivateDir(paths.StateDir()); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "other")
		if err := os.WriteFile(target, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, paths.HostID()); err != nil {
			t.Fatal(err)
		}
		if id, err := host.HostID(); err == nil {
			t.Fatalf("symlink host-id returned %q, want error", id)
		}
		st, err := os.Lstat(paths.HostID())
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink host-id was replaced: %v, %v", st, err)
		}
	}
}

func TestBridgeHelloCarriesHostID(t *testing.T) {
	isolateHostState(t)
	want, err := host.HostID()
	if err != nil {
		t.Fatal(err)
	}

	var in bytes.Buffer
	if err := proto.NewWriter(&in).WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := host.RunBridge(&in, &out); err != nil {
		t.Fatalf("RunBridge: %v", err)
	}
	f, err := proto.Read(bufio.NewReader(&out))
	if err != nil || f.Type != proto.THello {
		t.Fatalf("first frame = %v, %v", f.Type, err)
	}
	var hello proto.Hello
	if err := json.Unmarshal(f.Payload, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.HostID != want {
		t.Fatalf("hello.HostID = %q, want %q", hello.HostID, want)
	}

	// Corrupt host-id: bridge sends TError with code host_id.
	if err := os.WriteFile(paths.HostID(), []byte("not-hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Reset()
	out.Reset()
	_ = proto.NewWriter(&in).WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client",
	})
	if err := host.RunBridge(io.Reader(&in), &out); err == nil {
		t.Fatal("RunBridge succeeded with invalid host-id")
	}
	f, err = proto.Read(bufio.NewReader(&out))
	if err != nil || f.Type != proto.TError {
		t.Fatalf("error frame = %v, %v", f.Type, err)
	}
	var pe proto.Error
	if err := json.Unmarshal(f.Payload, &pe); err != nil || pe.Code != "host_id" {
		t.Fatalf("proto.Error = %+v, %v", pe, err)
	}
}

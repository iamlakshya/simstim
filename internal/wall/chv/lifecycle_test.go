package chv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestDriverOpenAndLifecycleStopDestroy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake ch-remote requires unix")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	if err := os.MkdirAll(filepath.Join(tmp, "run"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Stage a default runtime dir with a fake ch-remote script that answers
	// "info" while the API socket exists and deletes the socket on "power-button".
	rp := DefaultRuntimePaths()
	if err := os.MkdirAll(rp.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{rp.VMM, rp.Kernel} {
		if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	toolsPath := filepath.Join(rp.Dir, "tools.erofs")
	if err := os.WriteFile(toolsPath, []byte("tools"), 0o444); err != nil {
		t.Fatal(err)
	}
	toolsJSON, _ := json.Marshal(ToolsRecord{
		Path:       toolsPath,
		RootHash:   strings.Repeat("a", 64),
		DataBlocks: 100,
	})
	if err := os.WriteFile(toolsPath+".json", toolsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	fakeRemote := `#!/bin/sh
sock=""
cmd=""
while [ $# -gt 0 ]; do
  case "$1" in
    --api-socket) sock="$2"; shift 2;;
    *) cmd="$1"; shift;;
  esac
done
case "$cmd" in
  info)
    if [ -e "$sock" ]; then
      echo '{"state":"Running"}'
      exit 0
    fi
    exit 1
    ;;
  power-button|shutdown)
    rm -f "$sock"
    exit 0
    ;;
esac
exit 1
`
	if err := os.WriteFile(rp.CHRemote, []byte(fakeRemote), 0o755); err != nil {
		t.Fatal(err)
	}

	id := "life1234"
	if _, err := (Driver{}).Open(context.Background(), id); err == nil {
		t.Fatal("Open succeeded when VMM socket did not exist")
	}

	apiSock := paths.VMMSocket(id)
	vsockSock := paths.VsockSocket(id)
	diskPath := paths.SessionDisk(id)
	for _, p := range []string{apiSock, vsockSock, diskPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sb, err := (Driver{}).Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Driver.Open: %v", err)
	}
	if _, err := sb.Start(context.Background()); err == nil {
		t.Fatal("Start on already-running OpenedSandbox unexpectedly succeeded")
	}
	if err := sb.Stop(context.Background(), 3*time.Second); err != nil {
		t.Fatalf("OpenedSandbox.Stop: %v", err)
	}
	_ = sb.Wait()
	if err := sb.Destroy(); err != nil {
		t.Fatalf("OpenedSandbox.Destroy: %v", err)
	}
	for _, p := range []string{apiSock, vsockSock} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("socket %s still exists after Destroy: %v", p, err)
		}
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("session disk %s was removed by Destroy; stop/destroy must preserve it: %v", diskPath, err)
	}
}

func TestResolveRuntimeBundleStoredLabel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	if err := os.MkdirAll(paths.ArtefactsSHA256Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.ArtefactsRuntimeDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	writeArt := func(name, kind, payload string) api.RuntimeAsset {
		sum := sha256.Sum256([]byte(payload))
		h := hex.EncodeToString(sum[:])
		p := paths.ArtefactFile(h)
		if err := os.WriteFile(p, []byte(payload), 0o444); err != nil {
			t.Fatal(err)
		}
		return api.RuntimeAsset{
			Kind:   kind,
			Name:   name,
			Arch:   runtime.GOARCH,
			SHA256: h,
			Size:   int64(len(payload)),
		}
	}

	vmm := writeArt("cloud-hypervisor-"+runtime.GOARCH, "vmm", "vmm-bytes")
	rem := writeArt("ch-remote-"+runtime.GOARCH, "ch-remote", "rem-bytes")
	kern := writeArt("vmlinux-"+runtime.GOARCH, "kernel", "kern-bytes")
	tools := writeArt("tools-"+runtime.GOARCH+".erofs", "tools", "tools-bytes")
	toolsJSON, _ := json.Marshal(ToolsRecord{
		Path:       paths.ArtefactFile(tools.SHA256),
		RootHash:   strings.Repeat("b", 64),
		DataBlocks: 512,
	})
	if err := os.WriteFile(paths.ArtefactFile(tools.SHA256)+".json", toolsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := api.StoredRuntimeLabel{
		Runtime: RuntimeLabel,
		Items:   []api.RuntimeAsset{vmm, rem, kern, tools},
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(paths.RuntimeLabelFile(RuntimeLabel), b, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveRuntimeBundle(RuntimeLabel)
	if err != nil {
		t.Fatalf("ResolveRuntimeBundle: %v", err)
	}
	if got.VMM != paths.ArtefactFile(vmm.SHA256) ||
		got.CHRemote != paths.ArtefactFile(rem.SHA256) ||
		got.Kernel != paths.ArtefactFile(kern.SHA256) ||
		got.Tools.Path != paths.ArtefactFile(tools.SHA256) ||
		got.Tools.DataBlocks != 512 {
		t.Fatalf("unexpected bundle: %+v", got)
	}
	if len(got.Files) != 4 {
		t.Fatalf("expected 4 referenced artefact files, got %v", got.Files)
	}
}

func TestVerifySessionDiskCapSparseAllocatedBytes(t *testing.T) {
	mkfs, err := FindMkfsExt4(nil, nil)
	if err != nil {
		t.Skip("mkfs.ext4 not available")
	}
	disk := filepath.Join(t.TempDir(), "disk.img")
	const capBytes = 32 << 20
	created, err := EnsureSessionDisk(context.Background(), disk, capBytes, mkfs)
	if err != nil || !created {
		t.Fatalf("EnsureSessionDisk = (%v, %v)", created, err)
	}
	alloc, apparent, err := VerifySessionDiskCap(disk, capBytes)
	if err != nil {
		t.Fatalf("VerifySessionDiskCap: %v", err)
	}
	if apparent != capBytes {
		t.Fatalf("apparent = %d, want %d", apparent, capBytes)
	}
	if alloc <= 0 || alloc >= capBytes {
		t.Fatalf("sparse ext4 allocated bytes = %d, expected >0 and < %d", alloc, capBytes)
	}
	if _, _, err := VerifySessionDiskCap(disk, 1024); err == nil {
		t.Fatal("VerifySessionDiskCap succeeded with cap smaller than disk")
	}
}

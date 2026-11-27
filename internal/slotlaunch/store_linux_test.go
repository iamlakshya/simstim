//go:build linux

package slotlaunch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests use actual unprivileged temporary files and kernel flocks.
// They do not create accounts, cgroups, jails or a VMM, and prove no root boot.
func testBackend(t *testing.T) (*backend, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"store/sha256", "store/incoming", "slots", "sessions", "vms"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &backend{root: root, owner: os.Getuid(), caller: caller{uid: os.Getuid(), gid: os.Getgid(), name: "fixture"}}, dir
}

func contentRequest(data []byte) Request {
	hash := sha256.Sum256(data)
	return Request{Op: "store", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
}

func TestStoreRefusalsPublishNothingAndKeepExistingBytes(t *testing.T) {
	b, dir := testBackend(t)
	data := []byte("immutable runtime fixture")
	request := contentRequest(data)
	for _, payload := range [][]byte{data[:len(data)-1], bytes.Repeat([]byte{'x'}, len(data))} {
		if err := b.store(request, bytes.NewReader(payload)); err == nil {
			t.Fatal("accepted truncated or changed content")
		}
	}
	for _, name := range []string{"store/sha256", "store/incoming"} {
		entries, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil || len(entries) != 0 {
			t.Fatalf("refused payload left %s entries: %v %v", name, entries, err)
		}
	}
	if err := b.store(request, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "store/sha256", request.SHA256)
	if err := b.store(request, bytes.NewReader(bytes.Repeat([]byte{'x'}, len(data)))); err == nil {
		t.Fatal("existing store entry bypassed payload verification")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("refusal changed existing bytes")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("store protection: %v %v", info, err)
	}
}

func TestStoreNeverFollowsSymlinkOrAcceptsHardlink(t *testing.T) {
	b, dir := testBackend(t)
	data := []byte("fixture")
	r := contentRequest(data)
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, data, 0o444); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "store/sha256", r.SHA256)
	if err := os.Symlink(outside, name); err != nil {
		t.Fatal(err)
	}
	if err := b.store(r, bytes.NewReader(data)); err == nil {
		t.Fatal("accepted store symlink")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, name); err != nil {
		t.Fatal(err)
	}
	if err := b.store(r, bytes.NewReader(data)); err == nil {
		t.Fatal("accepted hard-linked store object")
	}
}

func TestSeventeenConcurrentReservationsUseSixteenActualLocks(t *testing.T) {
	b, _ := testBackend(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, 17)
	slots := make(chan int, 16)
	for range 17 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			slot, lock, err := b.acquireSlot()
			results <- err
			if err == nil {
				slots <- slot
				<-release
				lock.Close()
			}
		}()
	}
	close(start)
	success, noRoom := 0, 0
	for range 17 {
		err := <-results
		if err == nil {
			success++
			continue
		}
		var known *launchError
		if errors.As(err, &known) && known.Code == "no_room" {
			noRoom++
		} else {
			t.Errorf("unexpected reservation error: %v", err)
		}
	}
	if success != 16 || noRoom != 1 {
		t.Errorf("reservations: success=%d no_room=%d", success, noRoom)
	}
	seen := map[int]bool{}
	for range success {
		slot := <-slots
		if seen[slot] {
			t.Errorf("slot %d acquired twice", slot)
		}
		seen[slot] = true
	}
	close(release)
	wg.Wait()
	_, lock, err := b.acquireSlot()
	if err != nil {
		t.Fatal("released slot stayed busy", err)
	}
	lock.Close()
}

func TestForeignStoppedSessionCannotBeAdoptedOrPruned(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("fixture needs the ordinary non-root caller identity used in production")
	}
	b, dir := testBackend(t)
	data := []byte("retained image")
	r := contentRequest(data)
	if err := b.store(r, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "store/sha256", r.SHA256), time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	record := sessionRecord{Session: "abc234de", UID: b.caller.uid, GID: b.caller.gid, Digests: []string{r.SHA256}}
	if err := b.writeJSON("sessions/abc234de.json", record); err != nil {
		t.Fatal(err)
	}
	b.caller.uid++
	for _, create := range []bool{false, true} {
		if _, lock, err := b.session("abc234de", create); err == nil {
			lock.Close()
			t.Fatal("foreign caller adopted stopped session")
		}
	}
	removed, err := b.prune(nil)
	if err != nil || len(removed) != 0 {
		t.Fatalf("foreign prune removed retained content: %v %v", removed, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "store/sha256", r.SHA256))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("foreign prune changed retained content")
	}
}

func TestFixedDriverTemplatesContainOnlyDescriptorRolesAndPrivateEndpoints(t *testing.T) {
	r := validStart()
	for _, driver := range []string{"chv", "firecracker"} {
		r.Driver = driver
		args, config, err := fixedCommand(r, "/var/lib/runtime/vms/abc234de")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, "\n") + string(config)
		if args[0] != "/proc/self/fd/3" || strings.Contains(joined, "/bin/sh") || strings.Contains(joined, "--net") || strings.Contains(joined, "--no-seccomp") {
			t.Fatalf("unsafe template: %q", args)
		}
		if !strings.Contains(joined, "/proc/self/fd/8") {
			t.Fatal("mutable disk did not use its original held descriptor")
		}
	}
}

func TestCopyForkCheckpointAndRewindUseOnlyOneOwnedDisk(t *testing.T) {
	b, dir := testBackend(t)
	const original = "abc234de"
	const fork = "abc234df"
	if err := os.Mkdir(filepath.Join(dir, "vms", original), 0o770); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(dir, "vms", original, "disk.img")
	before := []byte("first checkpoint contents")
	if err := os.WriteFile(disk, before, 0o660); err != nil {
		t.Fatal(err)
	}
	record := sessionRecord{Session: original, UID: b.caller.uid, GID: b.caller.gid, SlotUID: b.caller.uid, Digests: []string{strings.Repeat("a", 64)}}
	if err := b.writeJSON("sessions/"+original+".json", record); err != nil {
		t.Fatal(err)
	}
	if err := b.copyDisk(original, fork); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "vms", fork, "disk.img"))
	if err != nil || !bytes.Equal(got, before) {
		t.Fatal("fork changed the disk bytes", err)
	}
	checkpoint := original + "/checkpoints/20261119T123456.789Z"
	if err := b.copyDisk(original, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("later mutable contents"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := b.copyDisk(checkpoint, original); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(disk)
	if err != nil || !bytes.Equal(got, before) {
		t.Fatal("rewind did not restore the owned checkpoint", err)
	}
	var saved sessionRecord
	if err := b.readJSON("sessions/"+fork+".json", &saved); err != nil || len(saved.Digests) != 1 || saved.Digests[0] != record.Digests[0] {
		t.Fatal("fork lost immutable retention references", err)
	}
}

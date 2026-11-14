//go:build linux

package chv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const diskTestBytes int64 = 16 << 20

func diskTestDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	dir := filepath.Join(root, "vm")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// diskMagicFixture is only an atomic-publication fixture. It is not a real
// ext4 filesystem and is never evidence that a VMM or real mkfs booted.
func diskMagicFixture(_ context.Context, file *os.File) error {
	_, err := file.WriteAt([]byte{0x53, 0xef}, 1024+56)
	return err
}

func existingDiskFixture(t *testing.T, dir string, bytes int64, header []byte) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, SessionDiskName), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(bytes); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(header, 0); err != nil {
		t.Fatal(err)
	}
}

func assertNoNewDiskFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".new-disk-") {
			t.Fatalf("uncommitted disk remains: %s", entry.Name())
		}
	}
}

func TestDiskNewPublicationIsWholeAndSparseSized(t *testing.T) {
	dir := diskTestDir(t)
	calls := 0
	info, err := EnsureDisk(context.Background(), dir, diskTestBytes, func(ctx context.Context, file *os.File) error {
		calls++
		if _, err := os.Lstat(filepath.Join(dir, SessionDiskName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new final disk was visible before formatting ended: %v", err)
		}
		stat, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if stat.Size() != diskTestBytes || stat.Mode().Perm() != 0o600 {
			t.Fatalf("new temporary disk %+v", stat)
		}
		return diskMagicFixture(ctx, file)
	})
	if err != nil || !info.Created || info.Bytes != diskTestBytes || calls != 1 {
		t.Fatalf("new disk %+v calls%d: %v", info, calls, err)
	}
	stat, err := os.Stat(filepath.Join(dir, SessionDiskName))
	if err != nil || stat.Size() != diskTestBytes || stat.Mode().Perm() != 0o600 {
		t.Fatalf("published disk %+v %v", stat, err)
	}
	assertNoNewDiskFiles(t, dir)
}

func TestDiskExistingQCOWHeaderIsNeverParsedOrFormatted(t *testing.T) {
	dir := diskTestDir(t)
	header := []byte("QFI\xfb\x00\x00\x00\x03/path/outside/vm")
	existingDiskFixture(t, dir, diskTestBytes, header)
	calls := 0
	info, err := EnsureDisk(context.Background(), dir, diskTestBytes, func(context.Context, *os.File) error { calls++; return errors.New("must not run") })
	if err != nil || info.Created || calls != 0 {
		t.Fatalf("existing raw disk %+v calls%d: %v", info, calls, err)
	}
	file, err := os.Open(filepath.Join(dir, SessionDiskName))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make([]byte, len(header))
	if _, err := file.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(header) {
		t.Fatal("guest-written header changed during reuse")
	}
	assertNoNewDiskFiles(t, dir)
}

func TestDiskExistingSizeMismatchIsNotResized(t *testing.T) {
	dir := diskTestDir(t)
	existingDiskFixture(t, dir, diskTestBytes, []byte("persistent"))
	calls := 0
	if _, err := EnsureDisk(context.Background(), dir, diskTestBytes+4096, func(context.Context, *os.File) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("mismatched disk was reformatted: calls%d err%v", calls, err)
	}
	stat, err := os.Stat(filepath.Join(dir, SessionDiskName))
	if err != nil || stat.Size() != diskTestBytes {
		t.Fatalf("existing disk resized: %+v %v", stat, err)
	}
}

func TestDiskUnsafeExistingTargetRefusesWithoutFormatter(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := diskTestDir(t)
			outside := filepath.Join(filepath.Dir(dir), "outside")
			if err := os.WriteFile(outside, []byte("do not change"), 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, SessionDiskName)
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, target); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			if _, err := EnsureDisk(context.Background(), dir, diskTestBytes, func(context.Context, *os.File) error { calls++; return nil }); err == nil || calls != 0 {
				t.Fatalf("unsafe %s accepted: calls%d %v", kind, calls, err)
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "do not change" {
				t.Fatalf("foreign target changed: %q %v", data, err)
			}
		})
	}
}

func TestDiskFormattingFailuresNeverPublishPartialFinal(t *testing.T) {
	for _, kind := range []string{"error", "missing ext4", "changed size", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			dir := diskTestDir(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("format failed")
			_, err := EnsureDisk(ctx, dir, diskTestBytes, func(ctx context.Context, file *os.File) error {
				switch kind {
				case "error":
					return sentinel
				case "missing ext4":
					return nil
				case "changed size":
					if err := diskMagicFixture(ctx, file); err != nil {
						return err
					}
					return file.Truncate(diskTestBytes + 1)
				case "cancelled":
					if err := diskMagicFixture(ctx, file); err != nil {
						return err
					}
					cancel()
					return nil
				}
				return nil
			})
			if err == nil {
				t.Fatal("failed formatter published a disk")
			}
			if kind == "error" && !errors.Is(err, sentinel) {
				t.Fatalf("lost format error: %v", err)
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, SessionDiskName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial final remains: %v", err)
			}
			assertNoNewDiskFiles(t, dir)
		})
	}
}

func TestDiskPublicationNeverReplacesAConcurrentWinner(t *testing.T) {
	dir := diskTestDir(t)
	winner := []byte("QFI\xfbconcurrent persistent disk")
	calls := 0
	info, err := EnsureDisk(context.Background(), dir, diskTestBytes, func(ctx context.Context, file *os.File) error {
		calls++
		if err := diskMagicFixture(ctx, file); err != nil {
			return err
		}
		existingDiskFixture(t, dir, diskTestBytes, winner)
		return nil
	})
	if err != nil || info.Created || calls != 1 {
		t.Fatalf("winner not preserved: %+v calls%d %v", info, calls, err)
	}
	file, err := os.Open(filepath.Join(dir, SessionDiskName))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make([]byte, len(winner))
	if _, err := file.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(winner) {
		t.Fatal("temporary file replaced the winner")
	}
	assertNoNewDiskFiles(t, dir)
}

func TestDiskCleanupDoesNotRemoveReplacedTemporaryTarget(t *testing.T) {
	dir := diskTestDir(t)
	replacement := ""
	_, err := EnsureDisk(context.Background(), dir, diskTestBytes, func(ctx context.Context, file *os.File) error {
		if err := diskMagicFixture(ctx, file); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".new-disk-") {
				replacement = filepath.Join(dir, entry.Name())
				if err := os.Rename(replacement, filepath.Join(dir, "moved-original")); err != nil {
					return err
				}
				return os.WriteFile(replacement, []byte("replacement must stay"), 0o600)
			}
		}
		return errors.New("temporary fixture missing")
	})
	if err == nil || replacement == "" {
		t.Fatalf("target substitution accepted: %v", err)
	}
	data, readErr := os.ReadFile(replacement)
	if readErr != nil || string(data) != "replacement must stay" {
		t.Fatalf("replacement removed during cleanup: %q %v", data, readErr)
	}
	if _, err := os.Lstat(filepath.Join(dir, SessionDiskName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("substitution created final disk: %v", err)
	}
}

func TestDiskPositiveSmallSizeReachesFormatterWithoutInventedMinimum(t *testing.T) {
	dir := diskTestDir(t)
	sentinel := errors.New("host formatter rejects small filesystem")
	calls := 0
	if _, err := EnsureDisk(context.Background(), dir, 1, func(context.Context, *os.File) error { calls++; return sentinel }); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("positive size bypassed formatter: calls%d %v", calls, err)
	}
	assertNoNewDiskFiles(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureDisk(ctx, dir, diskTestBytes, func(context.Context, *os.File) error { t.Error("cancelled formatter ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled disk: %v", err)
	}
}

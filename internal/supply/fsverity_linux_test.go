//go:build linux

package supply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func verityFixture(t *testing.T, kind string) (Item, *os.File, string) {
	t.Helper()
	data := []byte("fixed native runtime descriptor fixture")
	name := "cloud-hypervisor"
	asset := name
	if kind == KindKernel {
		name, asset = "guest", KernelAsset(runtime.GOARCH)
	}
	it := NewItem(kind, name, "6.18", runtime.GOARCH, data, base+asset)
	it.Released, it.EOL, it.HostKernel = "2026-09-28", "2027-01-06", ">=6.8"
	if kind == KindKernel {
		it.Licence, it.SourceURL = "GPL-2.0-only", base+KernelSourceAsset(it.Version)
	}
	path := filepath.Join(t.TempDir(), "runtime")
	mode := os.FileMode(0o444)
	if kind == KindVMM {
		mode = 0o555
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return it, f, path
}

func TestFSVerityLinuxUAPILayout(t *testing.T) {
	if unsafe.Sizeof(fsVerityEnableArg{}) != 128 || unsafe.Sizeof(fsVerityMeasurement{}) != 36 {
		t.Fatal("wrong fs-verity UAPI structure sizes")
	}
}

func TestInstallVerityRestoresModeAndUsesOriginalReadOnlyDescriptor(t *testing.T) {
	for _, enableErr := range []error{nil, unix.EOPNOTSUPP, unix.ENODATA, unix.EACCES, unix.EBUSY, unix.ETXTBSY} {
		t.Run(runtimeSealErrorName(enableErr), func(t *testing.T) {
			it, f, _ := verityFixture(t, KindKernel)
			measures, enables := 0, 0
			calls := verityCalls{measure: func(got *os.File) (string, error) {
				if got != f {
					t.Fatal("measurement changed descriptors")
				}
				measures++
				if measures == 1 {
					return "", unix.ENODATA
				}
				return it.FSVerity, nil
			}, enable: func(got *os.File) error {
				enables++
				flags, err := unix.FcntlInt(got.Fd(), unix.F_GETFL, 0)
				info, serr := got.Stat()
				if got != f || err != nil || serr != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || info.Mode().Perm() != 0o644 {
					t.Fatal("enable lost original read-only inode or temporary owner-write mode", err, serr)
				}
				return enableErr
			}}
			enabled, err := installRuntimeVerity(context.Background(), it, f, calls)
			info, serr := f.Stat()
			if serr != nil || info.Mode().Perm() != 0o444 || enables != 1 {
				t.Fatal("final store mode was not restored", serr, enables)
			}
			if enableErr == nil {
				if err != nil || !enabled || measures != 2 {
					t.Fatal(enabled, err, measures)
				}
			} else if errors.Is(enableErr, unix.EOPNOTSUPP) {
				if err != nil || enabled {
					t.Fatal(enabled, err)
				}
			} else if !errors.Is(err, enableErr) || enabled {
				t.Fatal("unexpected ioctl failure selected fallback", enabled, err)
			}
		})
	}
}

func TestInstallVerityMismatchAndDiskNeverEnable(t *testing.T) {
	it, f, _ := verityFixture(t, KindKernel)
	calls := verityCalls{measure: func(*os.File) (string, error) { return strings.Repeat("0", 64), nil }, enable: func(*os.File) error { t.Fatal("enabled already-measured inode"); return nil }}
	var mismatch *MismatchError
	if _, err := installRuntimeVerity(context.Background(), it, f, calls); !errors.As(err, &mismatch) {
		t.Fatal(err)
	}
	it.Kind, it.FSVerity = KindTools, ""
	calls.measure = func(*os.File) (string, error) { t.Fatal("measured disk"); return "", nil }
	if enabled, err := installRuntimeVerity(context.Background(), it, f, calls); enabled || err != nil {
		t.Fatal(enabled, err)
	}
}

func TestVerifiedKernelRetainsMeasuredInodeAcrossRename(t *testing.T) {
	it, f, path := verityFixture(t, KindKernel)
	calls := verityCalls{measure: func(*os.File) (string, error) { return it.FSVerity, nil }}
	v, err := prepareRuntime(context.Background(), it, f, calls)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	before, _ := f.Stat()
	f.Close()
	replacement := path + "-replacement"
	if err := os.WriteFile(replacement, []byte("other bytes"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	borrowed, err := v.File()
	if err != nil {
		t.Fatal(err)
	}
	after, err := borrowed.Stat()
	if err != nil || !os.SameFile(before, after) || !v.verity {
		t.Fatal("measured kernel was reopened or copied", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.File(); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedRuntimeMismatchDoesNotFallBack(t *testing.T) {
	for _, measurement := range []error{nil, unix.EIO, unix.EACCES, unix.EOVERFLOW} {
		it, f, _ := verityFixture(t, KindKernel)
		calls := verityCalls{measure: func(*os.File) (string, error) { return strings.Repeat("0", 64), measurement }}
		v, err := prepareRuntime(context.Background(), it, f, calls)
		if err == nil || v != nil {
			if v != nil {
				v.Close()
			}
			t.Fatal("measurement failure selected weaker path", measurement)
		}
		if measurement != nil && !errors.Is(err, measurement) {
			t.Fatal(err)
		}
	}
}

func TestVerifiedAbsentKernelUsesImmutableCopyAndVMMKeepsNoDumpMode(t *testing.T) {
	for _, kind := range []string{KindKernel, KindVMM} {
		it, f, path := verityFixture(t, kind)
		calls := verityCalls{measure: func(*os.File) (string, error) {
			if kind == KindVMM {
				return it.FSVerity, nil
			}
			return "", unix.ENODATA
		}}
		v, err := prepareRuntime(context.Background(), it, f, calls)
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		borrowed, err := v.File()
		if err != nil {
			t.Fatal(err)
		}
		info, err := borrowed.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if v.verity || info.Mode().Perm() != sealedRuntimeMode(kind) {
			t.Fatal("fallback/NoDump mode changed", kind, info.Mode())
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("mutated store bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := v.File(); err != nil {
			t.Fatal("store mutation changed sealed bytes", err)
		}
		if _, err := borrowed.WriteAt([]byte{1}, 0); !errors.Is(err, unix.EPERM) {
			t.Fatal("sealed copy was writable", err)
		}
	}
}

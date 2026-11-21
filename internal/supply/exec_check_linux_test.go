//go:build linux

package supply

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFinalKernelCheckUsesInheritedMeasuredInodeAfterUnlink(t *testing.T) {
	it, file, path := verityFixture(t, KindKernel)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	calls := 0
	op := verityCalls{measure: func(got *os.File) (string, error) {
		if got != file {
			t.Fatal("final check substituted the inherited descriptor")
		}
		calls++
		return it.FSVerity, nil
	}}
	if err := checkRuntimeDescriptor(t.Context(), it, file, op); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("final check did not retain and remeasure the same inode", calls)
	}
}

func TestFinalKernelCheckRefusesMeasurementSHAAndNativeRightsFailures(t *testing.T) {
	for _, kind := range []string{"measurement", "read-error", "sha", "mode", "writable-fd", "link"} {
		t.Run(kind, func(t *testing.T) {
			it, file, path := verityFixture(t, KindKernel)
			op := verityCalls{measure: func(*os.File) (string, error) { return it.FSVerity, nil }}
			switch kind {
			case "measurement":
				op.measure = func(*os.File) (string, error) { return strings.Repeat("0", 64), nil }
			case "read-error":
				op.measure = func(*os.File) (string, error) { return "", unix.EIO }
			case "sha":
				it.SHA256 = strings.Repeat("0", 64)
			case "mode":
				if err := file.Chmod(0o644); err != nil {
					t.Fatal(err)
				}
			case "writable-fd":
				if err := file.Chmod(0o644); err != nil {
					t.Fatal(err)
				}
				writer, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				if err := file.Chmod(0o444); err != nil {
					t.Fatal(err)
				}
				file = writer
			case "link":
				if err := os.Link(path, path+"-alias"); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkRuntimeDescriptor(t.Context(), it, file, op); err == nil {
				t.Fatal("final inherited check accepted", kind)
			}
		})
	}
}

func TestFinalKernelAbsenceUsesSealsAndVMMNeverAdmitsReadableMeasuredInode(t *testing.T) {
	it, file, _ := verityFixture(t, KindKernel)
	op := verityCalls{measure: func(*os.File) (string, error) { return "", unix.ENODATA }}
	if err := checkRuntimeDescriptor(t.Context(), it, file, op); err == nil {
		t.Fatal("an ordinary unsealed kernel became a fallback capability")
	}
	sealed, err := SealRuntime(t.Context(), it, file)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	if err := checkRuntimeDescriptor(t.Context(), it, sealed, op); err != nil {
		t.Fatal(err)
	}
	vmm, program, _ := verityFixture(t, KindVMM)
	op.measure = func(*os.File) (string, error) {
		t.Fatal("VMM final route tried direct readable verity")
		return vmm.FSVerity, nil
	}
	if err := checkRuntimeDescriptor(t.Context(), vmm, program, op); err == nil {
		t.Fatal("readable VMM bypassed the NoDump seal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkRuntimeDescriptor(ctx, it, sealed, op); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

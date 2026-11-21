//go:build linux

package publicationlease

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This admitted fixture crosses actual process creation/exec and parent
// death. EOF controls fixture shutdown only; fresh EX proves fence vacancy.
func TestPublicationNativeCreatorDeathRetainsDescendantFenceV1(t *testing.T) {
	switch os.Getenv("PUBLICATION_LEASE_CHILD_V1") {
	case "keeper":
		fence := os.NewFile(3, "original-descendant-fence")
		if err := CheckConverterFDV1(3); err != nil {
			os.Exit(81)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		fence.Close()
		os.Exit(0)
	case "creator":
		root, err := os.OpenRoot(os.Getenv("PUBLICATION_LEASE_ROOT_V1"))
		if err != nil {
			os.Exit(82)
		}
		source, err := CreateV1(t.Context(), root, nativeFixtureDomainV1(), strings.Repeat("c", 32))
		if err != nil {
			os.Exit(83)
		}
		loan, err := source.RetainForHelperV1(t.Context())
		if err != nil {
			os.Exit(84)
		}
		keeper := exec.Command(os.Args[0], "-test.run=^TestPublicationNativeCreatorDeathRetainsDescendantFenceV1$")
		keeper.Env = append(os.Environ(), "PUBLICATION_LEASE_CHILD_V1=keeper")
		keeper.ExtraFiles = []*os.File{loan}
		keeper.Stdin = os.NewFile(3, "original-fixture-shutdown")
		if err := keeper.Start(); err != nil {
			os.Exit(85)
		}
		_, _ = io.WriteString(os.Stdout, source.RecordV1())
		// Deliberately no Freeze, Close or Wait. The real creator dies while
		// its actual descendant retains the SAME original SH description.
		os.Exit(0)
	}
	if os.Getenv("PUBLICATION_LEASE_NATIVE_FIXTURE") != "1" {
		t.Skip("requires the admitted Linux publication process fixture")
	}
	root, domain, _ := publicationRootV1(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	created := -1
	creator := exec.Command(os.Args[0], "-test.run=^TestPublicationNativeCreatorDeathRetainsDescendantFenceV1$")
	creator.Env = append(os.Environ(), "PUBLICATION_LEASE_CHILD_V1=creator", "PUBLICATION_LEASE_ROOT_V1="+root.Name())
	creator.ExtraFiles = []*os.File{reader}
	creator.SysProcAttr = &syscall.SysProcAttr{PidFD: &created}
	data, err := creator.Output()
	if err != nil {
		t.Fatal(err)
	}
	if created < 0 {
		t.Fatal("fixture creator lacks its original atomic creation descriptor")
	}
	defer os.NewFile(uintptr(created), "original-fixture-creator-pidfd").Close()
	reader.Close()
	if _, err := TryExclusiveV1(t.Context(), root, domain, string(data)); !errors.Is(err, ErrBusy) {
		t.Fatal("parent death erased its surviving descendant", err)
	}
	writer.Close()
	until := time.Now().Add(5 * time.Second)
	for {
		proof, err := TryExclusiveV1(t.Context(), root, domain, string(data))
		if err == nil {
			defer proof.CloseV1()
			if err := proof.RemoveV1(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(until) {
			t.Fatal("original descendant did not release its native fence", err)
		}
		time.Sleep(time.Millisecond)
	}
}

//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWholeStopCannotOriginateFromDataOnlyOperationOrReceipt(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {id: "primary2"}} {
		source, err := StopSessionForOperationV1(context.Background(), operation)
		if err == nil || source != nil {
			t.Fatal("data-only operation issued a native stop source")
		}
	}
	for _, source := range []*StoppedSessionV1{nil, {}, {operation: &SessionOperation{id: "primary2"}, settled: true}} {
		if err := source.Check(context.Background(), &SessionOperation{id: "primary2"}); err == nil {
			t.Fatal("status/boolean supplied a physical stopped receipt")
		}
	}
}
func TestWholeStopRejectsOrdinaryPopulationFileAsNativeCgroup(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "cgroup.events"), []byte("populated 0\nfrozen 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	empty, err := originalStopCgroupEmptyV1(file)
	if err == nil || empty {
		t.Fatal("an ordinary empty-looking file became native whole-unit absence")
	}
}

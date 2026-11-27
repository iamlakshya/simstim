//go:build linux

package slotlaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSourceZeroAndOrdinaryFilesCannotIssueExecutionOrClosure(t *testing.T) {
	var source NativeSourceV1
	if source.SameSourceV1(context.Background(), validStart()) == nil {
		t.Fatal("zero source accepted a native start")
	}
	if source.PhysicalExitV1() {
		t.Fatal("zero source claimed native closure")
	}
	if _, err := source.DialGuestV1(context.Background()); err == nil {
		t.Fatal("zero source created a guest link")
	}
	name := filepath.Join(t.TempDir(), "ordinary")
	if err := os.WriteFile(name, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if pidExited(file) || pidLive(file) {
		t.Fatal("ordinary file became a native process witness")
	}
}

func TestResponseBufferRefusesBeforeGrowingPastBound(t *testing.T) {
	buffer := boundedResponse{data: make([]byte, RequestMax)}
	if _, err := buffer.Write([]byte{1}); err == nil {
		t.Fatal("oversized response accepted")
	}
	if len(buffer.data) != RequestMax {
		t.Fatal("refusal changed the bounded response")
	}
}

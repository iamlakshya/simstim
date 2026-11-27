//go:build windows

package client

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// A native reader that did not share deletion remains owned by that reader.
// Cleanup reports a genuine Windows sharing failure instead of closing it.
func TestHostStateRemovalKeepsNativeWindowsUnsharedReader(t *testing.T) {
	original, ctx, _, folder, validators := hostStateRemovalFixture(t)
	path := filepath.Join(folder, "baseline.json")
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	old := beforeHostStateUnlink
	defer func() { beforeHostStateUnlink = old }()
	attempted := false
	beforeHostStateUnlink = func(name string) {
		if name == "baseline.json" {
			attempted = true
		}
	}
	preserved, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators)
	if !attempted || err == nil || !slices.Contains(preserved.Preserved, path) {
		t.Fatalf("native sharing refusal missing: attempted=%v preserved=%+v error=%v", attempted, preserved, err)
	}
	buffer := make([]byte, len("owned:baseline.json"))
	var n uint32
	if err := windows.ReadFile(handle, buffer, &n, nil); err != nil || int(n) != len(buffer) || string(buffer) != "owned:baseline.json" {
		t.Fatalf("foreign reader closed or changed: %q,%d,%v", buffer, n, err)
	}
	if _, err := os.Stat(filepath.Join(folder, "removal.json")); err != nil {
		t.Fatalf("journal removed after native failure: %v", err)
	}
}

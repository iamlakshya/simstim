//go:build linux

package guest

import "testing"

func TestImageSaveRootsCannotOriginateFromMetadataVM(t *testing.T) {
	for _, runtime := range []*vmRuntime{{}, {connection: &vmInitConnection{}}} {
		root, upper, _, err := runtime.imageSaveRootsV1()
		if err == nil || root != nil || upper != nil {
			t.Fatal("metadata-only VM issued actual original overlay roots")
		}
	}
	files := newVMFiles(t.TempDir(), vmMountOps{})
	if err := files.captureImageSaveRootsV1(t.TempDir()); err != nil || files.imageSaveRoot != nil || files.imageSaveUpper != nil {
		t.Fatal("fixture filesystem calls minted native saved-root handles", err)
	}
}

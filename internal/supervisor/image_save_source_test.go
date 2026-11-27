package supervisor

import (
	"context"
	"testing"
)

func TestImageSaveUncertainCloseRetainsOriginalSourceOwner(t *testing.T) {
	// Negative ownership model only; no runtime/OP/root authority is supplied.
	done := make(chan struct{})
	close(done)
	root := &Supervisor{}
	job := &imageSaveJobV1{root: root, client: &client{}, ctx: context.Background(), cancel: func() {}, done: done, layer: &imageSaveLayerV1{uncertain: true}}
	root.imageSaves = map[*imageSaveJobV1]struct{}{job: {}}
	if err := job.Close(); err == nil {
		t.Fatal("logical close claimed uncertain native cleanup")
	}
	if _, held := root.imageSaves[job]; !held {
		t.Fatal("uncertain close forgot original source owner")
	}
}

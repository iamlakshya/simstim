package supply

import "fmt"

// MacRuntimeItemsV1 selects only the fixed signed Lima and base-image roles.
// Metadata validation is not permission to execute a runtime or change a Mac.
func MacRuntimeItemsV1(manifest Manifest) (Item, Item, error) {
	if err := manifest.Validate(); err != nil {
		return Item{}, Item{}, err
	}
	lima, found := manifest.Find(KindLima, "lima", "arm64")
	if !found || Below(lima.Version, "2.3.0") {
		return Item{}, Item{}, fmt.Errorf("this signed release has no Lima 2.3.0 or later for an Apple silicon Mac")
	}
	image, found := manifest.Find(KindVMImage, "lima-ubuntu", "arm64")
	if !found {
		return Item{}, Item{}, fmt.Errorf("this signed release has no pinned Linux base image for its Mac host")
	}
	if len(manifest.OwnFindings()) != 0 {
		return Item{}, Item{}, fmt.Errorf("the Mac host runtime matches a release advisory")
	}
	return lima, image, nil
}

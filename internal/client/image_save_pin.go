package client

import (
	"time"

	"github.com/iamlakshya/simstim/internal/image"
)

// recordSavedImagePin receives only the result checked against the original
// accepted host and ref. Its caller holds that source's short publication OP.
func recordSavedImagePin(lock *image.Lock, result image.SaveResult, ref, hostID, hostName string, replace bool, at time.Time) error {
	if result.Image.Ref != ref || result.HostID != hostID || !ValidHostID(hostID) || !image.ValidSaveRef(ref) {
		return imageSaveProtocolError()
	}
	if err := result.Validate(ref, hostID); err != nil {
		return imageSaveClientError(err)
	}
	index := -1
	for i, pin := range lock.Images {
		if pin.Ref == ref && pin.HostID == hostID {
			index = i
			break
		}
	}
	if index >= 0 && !replace {
		return &UserError{Code: "image_exists", Kind: KindFailed,
			Msg:  CleanLine(hostName) + " already has an image named " + CleanLine(ref),
			Hint: "Add --replace, or choose another saved image name."}
	}
	record := result.Image
	pin := image.Pin{
		Ref: ref, HostID: hostID, Digest: record.Digest, PinnedAt: at.UTC(),
		Platforms: []image.PlatformPin{{Arch: record.Arch, Digest: record.PlatformDigest, RootHash: record.RootHash, DataBlocks: record.DataBlocks, RootFrom: hostName}},
	}
	if index >= 0 {
		lock.Images[index] = pin
	} else {
		lock.Images = append(lock.Images, pin)
	}
	return nil
}

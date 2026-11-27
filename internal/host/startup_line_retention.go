package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This optional resource owner is fixed before the first physical reader.
// It changes only who records and closes rejected materialised file objects;
// it cannot supply a peer, role, Source or startup permission.
func (line *StartupLineV1) attachNativeRetirementV4(owner *nativeDescriptorRetirementsV1) error {
	if line == nil || owner == nil {
		return sessionLeaseError("the native reader has no original resource-retirement owner")
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.nativeReadStarted || line.reading || line.bytes != 0 || len(line.files) != 0 || line.cred != nil || line.nativeRetirement != nil || line.receiveRetention != nil {
		return sessionLeaseError("the original native reader cannot attach or replace retirement custody after reading")
	}
	line.nativeRetirement = owner
	line.receiveRetention = &proto.UnixControlRetentionV1{}
	return nil
}

func closeStartupReceivedFilesV4(owner *nativeDescriptorRetirementsV1, files []*os.File) error {
	if owner != nil {
		return owner.closeFilesV4(files)
	}
	var failure error
	for _, file := range files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

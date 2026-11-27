//go:build !linux

package host

import "os"

func sandboxPrepareMountIdentityV1(file *os.File) (uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	device, _, _, _, known := footprintIdentity(info)
	if !known {
		return 0, sessionLeaseError("prepare cleanup cannot verify this platform's native storage identity")
	}
	return device, nil
}

func sandboxPrepareLaunchCleanupConfirmedV1(operation *SessionOperation) bool {
	return operation.startup == nil
}

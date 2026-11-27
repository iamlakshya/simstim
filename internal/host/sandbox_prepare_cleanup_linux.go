//go:build linux

package host

import "os"

func sandboxPrepareMountIdentityV1(file *os.File) (uint64, error) {
	return stoppedCleanupMountV1(file)
}

func sandboxPrepareLaunchCleanupConfirmedV1(operation *SessionOperation) bool {
	launch := operation.startup
	return launch == nil || launch.operation == operation && (!launch.started || launch.cleanupConfirmed && launch.exited)
}

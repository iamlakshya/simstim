package host

import "time"

// SetExecutable makes holders start from path, for the rest of a test. It
// returns a function that puts the old one back.
func SetExecutable(path string) func() {
	old := holderExecutable
	holderExecutable = func() (string, error) { return path, nil }
	return func() { holderExecutable = old }
}

// ShadowVT returns the shadow terminal that the VT tests use, or "" when it
// is not built.
func ShadowVT() string {
	vtBinOnce.Do(findVTBinary)
	return vtBinPath
}

// ShadowVTSkip says why ShadowVT is "", for a test's skip.
func ShadowVTSkip() string {
	vtBinOnce.Do(findVTBinary)
	return vtBinSkip
}

// LockSession takes the lock of a session, as a resume, a restore and a
// delete do. It returns the function that lets it go.
func LockSession(id string) (func(), error) { return lockSession(id) }

// SetUploadIdle makes an upload wait d for its next frame, for the rest of
// a test. It returns a function that puts the old wait back.
func SetUploadIdle(d time.Duration) func() {
	old := uploadIdle.Swap(int64(d))
	return func() { uploadIdle.Store(old) }
}

// SetAfterUploadUntracked runs f right after an upload leaves b.uploads and
// before its file is removed or finalised, for the rest of a test. It returns
// a function that puts the old hook back.
func SetAfterUploadUntracked(f func()) func() {
	var ptr *func()
	if f != nil {
		ptr = &f
	}
	old := afterUploadUntracked.Swap(ptr)
	return func() { afterUploadUntracked.Store(old) }
}

// SetBeforeUploadReclaimWait runs f right before reclaimUploadsAt(true) waits
// on b.uploadsWG, for the rest of a test. It returns a function that puts the
// old hook back.
func SetBeforeUploadReclaimWait(f func()) func() {
	var ptr *func()
	if f != nil {
		ptr = &f
	}
	old := beforeUploadReclaimWait.Swap(ptr)
	return func() { beforeUploadReclaimWait.Store(old) }
}

// ReclaimBridgesUploads reclaims unfinished uploads across live bridges and
// waits for in-flight disk cleanup, as the idle-exit watchdog does.
func ReclaimBridgesUploads() { reclaimBridgesUploads() }

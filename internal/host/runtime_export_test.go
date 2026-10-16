package host

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

// LockSession takes the lock of a session, as a resume, a restore and a
// delete do. It returns the function that lets it go.
func LockSession(id string) (func(), error) { return lockSession(id) }

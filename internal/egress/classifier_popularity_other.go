//go:build !linux

package egress

import "os"

func newPopularitySnapshotV1() (*os.File, error)          { return nil, popularityErrorV1() }
func sealPopularitySnapshotV1(*os.File) (*os.File, error) { return nil, popularityErrorV1() }
func popularityOriginalVersionV1(*os.File) (popularityFileVersionV1, error) {
	return popularityFileVersionV1{}, popularityErrorV1()
}
func popularitySnapshotVersionV1(*os.File) (popularityFileVersionV1, error) {
	return popularityFileVersionV1{}, popularityErrorV1()
}
func popularityCloseOnExecV1(*os.File) {}

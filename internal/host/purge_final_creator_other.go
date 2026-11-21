//go:build !linux

package host

import "os"

func retainCreatedPurgeFinalMarkerV2(*purgeOwnerV1, *os.Root, *os.File, string) (bool, error) {
	return false, nil
}
func sealCreatedPurgeFinalMarkerV2(*purgeOwnerV1, []byte) error { return nil }

func reuseCreatedPurgeFinalMarkerV2(*purgeOwnerV1, *os.Root) (*os.File, string, string, bool, error) {
	return nil, "", "", false, nil
}

func actualPurgeFinalMarkerReadyV2(*purgeOwnerV1) (bool, error) { return true, nil }

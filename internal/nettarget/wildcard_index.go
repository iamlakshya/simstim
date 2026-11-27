package nettarget

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const (
	maxPSLIndexBytes   = 256 << 10
	maxPSLIndexEntries = 4096
)

type pslAncestorIndex struct {
	parents []string
}

var pslAncestors, pslAncestorsReady = readPSLAncestorIndex(pslAncestorData, publicsuffix.List.String())

func readPSLAncestorIndex(body, libraryVersion string) (pslAncestorIndex, bool) {
	if libraryVersion != pslDataVersion || len(body) > maxPSLIndexBytes || !strings.HasSuffix(body, "\n") {
		return pslAncestorIndex{}, false
	}
	digest := sha256.Sum256([]byte(body))
	if hex.EncodeToString(digest[:]) != pslIndexBodySHA256 {
		return pslAncestorIndex{}, false
	}
	parents := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(parents) != pslIndexCount || len(parents) > maxPSLIndexEntries {
		return pslAncestorIndex{}, false
	}
	for index, parent := range parents {
		canonical, err := NormaliseHost(parent)
		if err != nil || canonical != parent || isNumericAddr(parent) || (index > 0 && parents[index-1] >= parent) {
			return pslAncestorIndex{}, false
		}
	}
	return pslAncestorIndex{parents: parents}, true
}

func (index pslAncestorIndex) contains(host string) bool {
	_, found := slices.BinarySearch(index.parents, host)
	return found
}

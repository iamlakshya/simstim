package nettarget

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

func properDomainSuffix(host, suffix string) bool {
	return len(host) > len(suffix) && strings.HasSuffix(host, "."+suffix)
}

// Named suffix descendants need the complete pinned ancestor index. Finite
// child witnesses cannot discover them. The library still decides the base
// and wildcard-anchor cases, including private suffixes and exceptions.
// Its ICANN flag cannot distinguish private hosting from an unknown TLD.
func safeWildcardBoundary(host string) bool {
	if !pslAncestorsReady || pslAncestors.contains(host) {
		return false
	}
	suffix, _ := publicsuffix.PublicSuffix(host)
	if !properDomainSuffix(host, suffix) {
		return false
	}
	for _, label := range []string{"a", "b"} {
		childSuffix, _ := publicsuffix.PublicSuffix(label + "." + host)
		if !properDomainSuffix(host, childSuffix) {
			return false
		}
	}
	return true
}

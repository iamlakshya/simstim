//go:build windows || darwin

package gitsource

import (
	"golang.org/x/text/unicode/norm"
	"strings"
)

// Desktop path refusal is conservative on volumes whose case sensitivity
// is unknown. Normalisation changes only comparison keys, never OS paths.
func sourcePathKey(name string) string { return strings.ToLower(norm.NFC.String(name)) }

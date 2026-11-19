package image

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

func savedAbsolutePath(name string) bool {
	if !utf8.ValidString(name) || !path.IsAbs(name) || path.Clean(name) != name || len(name) > 4096 || strings.ContainsRune(name, '\\') {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

// SaveRootPath names a clean tar path without interpreting links on the host.
func SaveRootPath(name string) (string, error) {
	if !savedAbsolutePath(name) {
		return "", saveFailure("image_bad_layer", "the saved-root path is invalid")
	}
	return name, nil
}

package nettarget

import (
	"strings"
)

// CanonicalPath decodes one path encoding layer and refuses ambiguous routes.
// Its result can pass through this function again without changing.
func CanonicalPath(raw string) (string, error) {
	var decoded strings.Builder
	validator := NewPathValidatorV1(&decoded)
	if _, err := validator.Write([]byte(raw)); err != nil {
		return "", err
	}
	if err := validator.Finish(); err != nil {
		return "", err
	}
	return decoded.String(), nil
}

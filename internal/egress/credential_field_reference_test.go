package egress

import (
	"encoding/base64"
	"github.com/iamlakshya/simstim/internal/api"
	"strings"
)

// This is the exact pre-delegation production formatter. Keep it independent
// of the new stream predicate for differential and reversion evidence.
func legacyRequestCredentialValue(kind, user string, value api.Secret) (api.Secret, bool) {
	var result string
	switch kind {
	case "raw":
		result = string(value)
	case "bearer":
		padding := false
		characters := false
		for i := range len(value) {
			b := value[i]
			if b == '=' {
				padding = true
				continue
			}
			if padding || !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~+/", rune(b))) {
				return "", false
			}
			characters = true
		}
		if !characters {
			return "", false
		}
		result = "Bearer " + string(value)
	case "basic":
		result = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+string(value)))
	default:
		return "", false
	}
	if strings.Trim(result, " \t") != result {
		return "", false
	}
	for i := range len(result) {
		if result[i] < 0x20 && result[i] != '\t' || result[i] == 0x7f {
			return "", false
		}
	}
	return api.Secret(result), true
}

// Package credentialpaths defines the home paths the paste guard refuses.
// Image export uses the same inert names without consulting the host's home.
package credentialpaths

import "strings"

// Folders returns owned home-relative private folder names.
func Folders() []string {
	return []string{".ssh", ".aws", ".config", ".gnupg", ".kube", ".docker", "Library/Keychains"}
}

// PrivateName gives the paste guard's reason for a credential-like basename.
// Comparisons preserve its Unicode case-folding behaviour.
func PrivateName(name string) string {
	pattern := ""
	switch {
	case strings.EqualFold(name, ".env"):
		pattern = ".env"
	case foldPrefix(name, ".env.") && !EnvTemplate(name):
		pattern = ".env.*"
	case foldSuffix(name, ".pem"):
		pattern = "*.pem"
	case foldSuffix(name, ".key"):
		pattern = "*.key"
	case foldPrefix(name, "id_"):
		pattern = "id_*"
	case foldPrefix(name, "credentials"):
		pattern = "credentials*"
	default:
		return ""
	}
	return "has a private name (" + pattern + ")"
}

// EnvTemplate recognises the three non-secret example spellings.
func EnvTemplate(name string) bool {
	for _, template := range []string{".env.example", ".env.sample", ".env.template"} {
		if strings.EqualFold(name, template) {
			return true
		}
	}
	return false
}

func foldPrefix(value, prefix string) bool {
	runes, wanted := []rune(value), []rune(prefix)
	return len(runes) >= len(wanted) && strings.EqualFold(string(runes[:len(wanted)]), prefix)
}

func foldSuffix(value, suffix string) bool {
	runes, wanted := []rune(value), []rune(suffix)
	return len(runes) >= len(wanted) && strings.EqualFold(string(runes[len(runes)-len(wanted):]), suffix)
}

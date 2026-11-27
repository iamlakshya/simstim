package egress

import (
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func requireSelectedPrefix(kind, name string, secret registeredSecret, proposed api.Secret, selectedOnly bool) error {
	if selectedOnly {
		selected, known := agents.Find(kind)
		found := false
		if known {
			for _, row := range selected.SecretBindings() {
				found = found || row.Env == name
			}
		}
		if !found {
			return nil
		}
	}
	prefix, err := registryPrefix(kind, name)
	if err != nil {
		return err
	}
	if secret.bound == nil || !shapeKeepsPrefix(secret.bound.shape, prefix) || !strings.HasPrefix(string(secret.bound.sentinel), prefix) || !strings.HasPrefix(string(proposed), prefix) || secret.present && !strings.HasPrefix(string(secret.value), prefix) {
		return memberError("secret_shape", "secret "+name+" cannot preserve the selected member's trusted format; replacement did not occur", "Stop and resume or start a fresh sandbox with the intended selected credential format.")
	}
	return nil
}

func shapeKeepsPrefix(shape sentinelShape, prefix string) bool {
	if strings.HasPrefix(shape.Prefix, prefix) {
		return true
	}
	if !strings.HasPrefix(prefix, shape.Prefix) {
		return false
	}
	var literal strings.Builder
	literal.WriteString(shape.Prefix)
	for _, slot := range shape.Slots {
		if utf8.RuneCountInString(slot.Alphabet) != 1 {
			return false
		}
		for range slot.Count {
			literal.WriteString(slot.Alphabet)
			if literal.Len() >= len(prefix) {
				return strings.HasPrefix(literal.String(), prefix)
			}
		}
	}
	return false
}

func checkSavedPrefix(name, prefix string) error {
	valid, err := agents.ValidSavedSecretPrefix(name, prefix)
	if err != nil || !valid {
		return memberError("secret_shape", "saved public prefix is not canonical for secret "+name, "Use the original supported public format or start a fresh approved session.")
	}
	return nil
}

package agents

import "errors"

// SecretPrefixForAgent uses the selected trusted agent's normative data.
// Other callers receive only the verified unanimous global fallback. A
// prefix adds neither a fetch name nor an outbound destination permission.
func SecretPrefixForAgent(kind, name string) (string, error) {
	defaults, err := GlobalSecretDefaults()
	if err != nil {
		return "", err
	}
	prefix := defaults[name].Prefix
	selected, known := Find(kind)
	if !known {
		return prefix, nil
	}
	found := false
	for _, row := range selected.SecretBindings() {
		if row.Env != name {
			continue
		}
		if found {
			return "", errors.New("selected agent secret prefix metadata is ambiguous")
		}
		found = true
		prefix = row.Prefix
	}
	return prefix, nil
}

// ValidSavedSecretPrefix validates only a supplied public format constraint.
// It never chooses an agent, supplies a default or authorises a secret name.
func ValidSavedSecretPrefix(name, prefix string) (bool, error) {
	if _, err := GlobalSecretDefaults(); err != nil {
		return false, err
	}
	if prefix == "" {
		return true, nil
	}
	for _, agent := range Table {
		for _, row := range agent.SecretBindings() {
			if row.Env == name && row.Prefix == prefix {
				return true, nil
			}
		}
	}
	return false, nil
}

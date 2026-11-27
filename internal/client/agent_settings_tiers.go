package client

import (
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func agentString(value tomlinspect.ValueCursor) (tomlinspect.Text, error) {
	scalar, err := value.Scalar()
	if err != nil {
		return tomlinspect.Text{}, err
	}
	text, ok := scalar.Text()
	if !ok {
		return tomlinspect.Text{}, errAgentSettingsType
	}
	return text, nil
}

func (v *agentSettingsVisitor) secretTiers(value tomlinspect.ValueCursor) error {
	table, err := value.Table()
	if err != nil {
		return err
	}
	return table.Walk(func(path tomlinspect.PathCursor, tierValue tomlinspect.ValueCursor) error {
		name, err := path.Name()
		if err != nil {
			return err
		}
		var names secretNameValidation
		if err := name.Walk(func(r rune) error { names.Rune(r); return nil }); err != nil {
			return err
		}
		if names.Check(true) != secretNameOK {
			return secretDefinitionError("config.toml contains an invalid secret tier name")
		}
		text, err := agentString(tierValue)
		if err != nil {
			return err
		}
		return validateAgentTier(text)
	})
}

func validateAgentTier(text tomlinspect.Text) error {
	// Padding can be arbitrary. The recognised canonical tierNames are
	// intrinsically short; only their normalised candidate is retained.
	var token [32]byte
	used := 0
	started, trailing, invalid := false, false, false
	err := text.Walk(func(r rune) error {
		if unicode.IsSpace(r) {
			if started {
				trailing = true
			}
			return nil
		}
		if trailing {
			invalid = true
		}
		started = true
		r = unicode.ToLower(r)
		width := utf8.RuneLen(r)
		if width < 1 || width > len(token)-used {
			invalid = true
			return nil
		}
		used += utf8.EncodeRune(token[used:], r)
		return nil
	})
	if err != nil {
		return err
	}
	if invalid {
		return secretDefinitionError("config.toml contains an invalid secret tier")
	}
	for _, candidate := range tierNames {
		if len(candidate) != used || candidate == "" {
			continue
		}
		equal := true
		for index := range len(candidate) {
			equal = equal && candidate[index] == token[index]
		}
		if equal {
			if _, err := ParseTier(candidate); err == nil {
				return nil
			}
		}
	}
	return secretDefinitionError("config.toml contains an invalid secret tier")
}

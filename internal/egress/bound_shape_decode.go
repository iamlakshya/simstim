package egress

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func decodePublicSecretShape(ctx context.Context, name string, public *api.SecretShape, policy sentinelPunctuationPolicy) (sentinelShape, error) {
	shape := sentinelShape{Prefix: public.Prefix}
	unicodeClasses := make(map[sentinelClass]string)
	for _, slot := range public.Slots {
		alphabet, err := decodePublicSentinelClass(ctx, name, slot, unicodeClasses)
		if err != nil {
			return sentinelShape{}, err
		}
		if public.Rule == "" && !genericPublicClass(slot, policy) {
			return sentinelShape{}, sentinelFailure(name, "saved generic class is not a trusted source class")
		}
		if slot.Count <= 0 || len(shape.Slots) != 0 && shape.Slots[len(shape.Slots)-1].Alphabet == alphabet {
			return sentinelShape{}, sentinelFailure(name, "saved token run is not canonical")
		}
		shape.Slots = append(shape.Slots, sentinelSlot{Alphabet: alphabet, Count: slot.Count})
	}
	canonical, err := publicSecretShape(ctx, name, shape, public.Rule)
	if err != nil || !samePublicSecretShape(canonical, public) {
		return sentinelShape{}, sentinelFailure(name, "saved token class encoding is not canonical")
	}
	return shape, nil
}

func genericPublicClass(slot api.SecretShapeSlot, policy sentinelPunctuationPolicy) bool {
	switch slot.Class {
	case "ascii-upper", "ascii-lower", "ascii-digit", "unicode-upper", "unicode-lower", "unicode-digit":
		return true
	case "ascii-printable-punct":
		return policy == sentinelPunctuationPrintable
	case "ascii-token68-punct":
		return policy == sentinelPunctuationToken68
	case "ascii":
		return len(slot.ASCII) == 1 && strings.Contains(sentinelSeparators, slot.ASCII)
	default:
		return false
	}
}

func decodePublicSentinelClass(ctx context.Context, name string, slot api.SecretShapeSlot, unicodeClasses map[sentinelClass]string) (string, error) {
	classes := map[string]string{
		"ascii-upper": sentinelUpper, "ascii-lower": sentinelLower, "ascii-digit": sentinelDigits,
		"ascii-printable-punct": sentinelPrintablePunctuation, "ascii-token68-punct": sentinelToken68Punctuation,
	}
	if alphabet, present := classes[slot.Class]; present && slot.Width == 1 && slot.ASCII == "" {
		return alphabet, nil
	}
	if slot.Class == "ascii" && slot.Width == 1 && slot.ASCII != "" {
		for _, r := range slot.ASCII {
			if r >= utf8.RuneSelf || !sentinelRune(r) {
				return "", sentinelFailure(name, "saved ASCII token class is invalid")
			}
		}
		return slot.ASCII, nil
	}
	categories := map[string]byte{"unicode-upper": 'U', "unicode-lower": 'L', "unicode-digit": 'D'}
	if category := categories[slot.Class]; category != 0 && slot.Width >= 2 && slot.Width <= utf8.UTFMax && slot.ASCII == "" {
		key := sentinelClass{category: category, width: slot.Width}
		if alphabet := unicodeClasses[key]; alphabet != "" {
			return alphabet, nil
		}
		alphabet, err := sentinelUnicodeAlphabet(ctx, name, key)
		if err != nil {
			return "", err
		}
		unicodeClasses[key] = alphabet
		return alphabet, nil
	}
	return "", sentinelFailure(name, "saved token class is unsupported")
}

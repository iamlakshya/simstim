package egress

import (
	"context"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func publicSecretShape(ctx context.Context, name string, shape sentinelShape, rule string) (*api.SecretShape, error) {
	output := &api.SecretShape{Version: api.SecretShapeV1, Prefix: shape.Prefix, Rule: rule}
	unicodeClasses := make(map[sentinelClass]string)
	for _, slot := range shape.Slots {
		class, width, ascii, err := publicSentinelClass(ctx, name, slot.Alphabet, unicodeClasses)
		if err != nil {
			return nil, err
		}
		output.Slots = append(output.Slots, api.SecretShapeSlot{Class: class, Width: width, Count: slot.Count, ASCII: ascii})
	}
	return output, nil
}

func publicSentinelClass(ctx context.Context, name, alphabet string, unicodeClasses map[sentinelClass]string) (string, int, string, error) {
	for _, known := range []struct{ alphabet, class string }{
		{sentinelUpper, "ascii-upper"}, {sentinelLower, "ascii-lower"},
		{sentinelDigits, "ascii-digit"}, {sentinelPrintablePunctuation, "ascii-printable-punct"},
		{sentinelToken68Punctuation, "ascii-token68-punct"},
	} {
		if alphabet == known.alphabet {
			return known.class, 1, "", nil
		}
	}
	ascii := alphabet != ""
	for _, value := range alphabet {
		ascii = ascii && value < utf8.RuneSelf && sentinelRune(value)
	}
	if ascii {
		return "ascii", 1, alphabet, nil
	}
	first, _ := utf8.DecodeRuneInString(alphabet)
	width := utf8.RuneLen(first)
	class, category := "", byte(0)
	switch {
	case unicode.Is(unicode.Lu, first):
		class, category = "unicode-upper", 'U'
	case unicode.Is(unicode.Ll, first):
		class, category = "unicode-lower", 'L'
	case unicode.Is(unicode.Nd, first):
		class, category = "unicode-digit", 'D'
	}
	if category != 0 {
		key := sentinelClass{category, width}
		canonical := unicodeClasses[key]
		if canonical == "" {
			var err error
			canonical, err = sentinelUnicodeAlphabet(ctx, name, key)
			if err != nil {
				return "", 0, "", err
			}
			unicodeClasses[key] = canonical
		}
		if canonical == alphabet {
			return class, width, "", nil
		}
	}
	return "", 0, "", sentinelFailure(name, "token class has no safe compact representation")
}

// Restore preserves source classes. A random punctuation sample can itself be
// a separator, so it is never used to infer a generic class a second time.
func restoreSecretShape(ctx context.Context, name string, saved api.PublicSecretState, prefix string, policy sentinelPunctuationPolicy) (sentinelShape, error) {
	if saved.Mode != api.SecretModeBound || saved.Sentinel == "" || saved.Shape == nil || saved.Shape.Version != api.SecretShapeV1 || saved.Shape.Prefix != prefix {
		return sentinelShape{}, sentinelFailure(name, "saved public token metadata is invalid")
	}
	pattern, err := secretRuleExpression(saved.Shape.Rule)
	if err != nil {
		return sentinelShape{}, err
	}
	shape, err := decodePublicSecretShape(ctx, name, saved.Shape, policy)
	if err != nil {
		return sentinelShape{}, err
	}
	if err := checkSentinelValue(ctx, name, saved.Sentinel, shape); err != nil {
		return sentinelShape{}, err
	}
	if pattern != "" {
		if err := validateSavedGrammarShape(ctx, name, shape, pattern); err != nil {
			return sentinelShape{}, err
		}
	}
	return shape, nil
}

func secretRuleExpression(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		return "", err
	}
	for _, rule := range rules.rules {
		if rule.id == id {
			return rule.re.String(), nil
		}
	}
	return "", memberError("bad_secret", "saved token grammar is unsupported", "Start a fresh sandbox with a supported credential format.")
}

func compileBoundShape(ctx context.Context, name string, value api.Secret, prefix string, policy sentinelPunctuationPolicy) (sentinelShape, string, error) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		return sentinelShape{}, "", err
	}
	for _, rule := range rules.rules {
		indexes := rule.re.FindStringSubmatchIndex(string(value))
		if len(indexes) == 4 && indexes[2] == 0 && indexes[3] == len(value) {
			shape, err := compileSentinelFormat(ctx, name, value, prefix, rule.re.String(), policy)
			return shape, rule.id, err
		}
	}
	shape, err := compileSentinelFormat(ctx, name, value, prefix, "", policy)
	return shape, "", err
}

func sameSecretShape(a, b sentinelShape) bool {
	return a.Prefix == b.Prefix && reflect.DeepEqual(a.Slots, b.Slots)
}

func validBoundCredential(value api.Secret) bool {
	return value != "" && !strings.ContainsAny(string(value), "\x00\r\n")
}

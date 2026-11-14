package egress

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type sentinelPunctuationPolicy uint8

const (
	sentinelPunctuationUnset sentinelPunctuationPolicy = iota
	sentinelPunctuationPrintable
	sentinelPunctuationToken68
)

const (
	sentinelUpper                = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	sentinelLower                = "abcdefghijklmnopqrstuvwxyz"
	sentinelDigits               = "0123456789"
	sentinelSeparators           = "-_./+=:"
	sentinelPrintablePunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	sentinelToken68Punctuation   = "-._~+/"
)

// compileSentinelFormat compiles only an already-authorised value's format.
// It recognises no credential in a response and supplies no binding/grant.
// A reviewed regex is authoritative: mismatch never falls back to inference.
func compileSentinelFormat(ctx context.Context, name string, value api.Secret, prefix, reviewedCaptureRegex string, policy sentinelPunctuationPolicy) (sentinelShape, error) {
	if !validRegistryName(name) {
		return sentinelShape{}, &proto.Error{Code: "bad_secret", Message: "invalid secret name for format compilation", Hint: "Use the approved canonical environment name."}
	}
	if err := sentinelContext(ctx, name); err != nil {
		return sentinelShape{}, err
	}
	if len(value) > proto.MaxFrame || !utf8.ValidString(string(value)) || !utf8.ValidString(prefix) || !strings.HasPrefix(string(value), prefix) {
		return sentinelShape{}, sentinelFailure(name, "value does not match a representable known prefix")
	}
	var shape sentinelShape
	var err error
	if reviewedCaptureRegex != "" {
		shape, err = compileReviewedSentinel(ctx, name, value, prefix, reviewedCaptureRegex)
	} else {
		shape, err = compileGenericSentinel(ctx, name, value, prefix, policy)
	}
	if err != nil {
		return sentinelShape{}, err
	}
	if err := checkSentinelValue(ctx, name, value, shape); err != nil {
		return sentinelShape{}, err
	}
	return shape, nil
}

func appendSentinelRun(slots []sentinelSlot, alphabet string, count int) []sentinelSlot {
	if count == 0 {
		return slots
	}
	if len(slots) != 0 && slots[len(slots)-1].Alphabet == alphabet {
		slots[len(slots)-1].Count += count
		return slots
	}
	return append(slots, sentinelSlot{Alphabet: alphabet, Count: count})
}

type sentinelClass struct {
	category byte
	width    int
}

func compileGenericSentinel(ctx context.Context, name string, value api.Secret, prefix string, policy sentinelPunctuationPolicy) (sentinelShape, error) {
	var punctuation string
	switch policy {
	case sentinelPunctuationPrintable:
		punctuation = sentinelPrintablePunctuation
	case sentinelPunctuationToken68:
		punctuation = sentinelToken68Punctuation
	default:
		return sentinelShape{}, sentinelFailure(name, "generic token punctuation policy is missing")
	}
	shape := sentinelShape{Prefix: prefix}
	classes := make(map[sentinelClass]string)
	for _, r := range string(value)[len(prefix):] {
		if err := sentinelContext(ctx, name); err != nil {
			return sentinelShape{}, err
		}
		var alphabet string
		switch {
		case r >= 'A' && r <= 'Z':
			alphabet = sentinelUpper
		case r >= 'a' && r <= 'z':
			alphabet = sentinelLower
		case r >= '0' && r <= '9':
			alphabet = sentinelDigits
		case strings.ContainsRune(sentinelSeparators, r):
			// These structural positions are explicitly public shape. This
			// fixed set is source policy, not the observed value's alphabet.
			alphabet = string(r)
		case r < utf8.RuneSelf:
			if !strings.ContainsRune(punctuation, r) {
				return sentinelShape{}, sentinelFailure(name, "character is outside the trusted punctuation policy")
			}
			alphabet = punctuation
		default:
			category := byte(0)
			switch {
			case unicode.Is(unicode.Lu, r):
				category = 'U'
			case unicode.Is(unicode.Ll, r):
				category = 'L'
			case unicode.Is(unicode.Nd, r):
				category = 'D'
			default:
				return sentinelShape{}, sentinelFailure(name, "Unicode character category has no reviewed token shape")
			}
			key := sentinelClass{category, utf8.RuneLen(r)}
			alphabet = classes[key]
			if alphabet == "" {
				var err error
				alphabet, err = sentinelUnicodeAlphabet(ctx, name, key)
				if err != nil {
					return sentinelShape{}, err
				}
				classes[key] = alphabet
			}
		}
		shape.Slots = appendSentinelRun(shape.Slots, alphabet, 1)
	}
	return shape, nil
}

// The pinned standard Unicode tables define a complete class, not a set of
// observed value codepoints. Width filtering preserves byte positions. Other
// categories, including marks, uncased letters and symbols, are unsupported.
func sentinelUnicodeAlphabet(ctx context.Context, name string, class sentinelClass) (string, error) {
	var table *unicode.RangeTable
	switch class.category {
	case 'U':
		table = unicode.Lu
	case 'L':
		table = unicode.Ll
	case 'D':
		table = unicode.Nd
	default:
		return "", sentinelFailure(name, "unsupported Unicode token class")
	}
	var out strings.Builder
	var steps int
	add := func(cp rune) error {
		steps++
		if steps%128 == 0 {
			if err := sentinelContext(ctx, name); err != nil {
				return err
			}
		}
		if utf8.RuneLen(cp) == class.width && sentinelRune(cp) {
			out.WriteRune(cp)
		}
		return nil
	}
	for _, span := range table.R16 {
		for cp := uint32(span.Lo); cp <= uint32(span.Hi); cp += uint32(span.Stride) {
			if err := add(rune(cp)); err != nil {
				return "", err
			}
		}
	}
	for _, span := range table.R32 {
		for cp := span.Lo; cp <= span.Hi; cp += span.Stride {
			if err := add(rune(cp)); err != nil {
				return "", err
			}
		}
	}
	if out.Len() == 0 {
		return "", sentinelFailure(name, "Unicode token class has no same-width alphabet")
	}
	return out.String(), nil
}

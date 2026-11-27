package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// sentinelShape is an internal source-backed format, not a wire schema. The
// registry owns compact persistence. Alphabets are reviewed classes or token
// grammars, never the observed literal characters of a real value.
type sentinelShape struct {
	Prefix string
	Slots  []sentinelSlot
}

// Count is run length, so a class alphabet need not be repeated per character.
// A singleton alphabet represents a source-backed literal separator. All its
// runes have equal UTF8 width, preserving exact byte length at every position.
type sentinelSlot struct {
	Alphabet string
	Count    int
}

type sentinelGroup struct {
	alphabet []rune
	allowed  map[rune]struct{}
	count    int
	width    int
}

type sentinelPlan struct {
	prefix   string
	groups   []sentinelGroup
	bytes    int
	variable bool
}

func (sentinelShape) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (sentinelSlot) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }
func (sentinelPlan) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }

func sentinelFailure(name, reason string) *proto.Error {
	return &proto.Error{Code: "secret_sentinel", Message: "secret " + name + ": " + reason,
		Hint: "Use a supported credential format. Approve --expose " + name + " only if the tool accepts an environment credential."}
}

func sentinelContext(ctx context.Context, name string) error {
	if ctx == nil {
		return &proto.Error{Code: "bad_request", Message: "sentinel generation has no context", Hint: "Retry using matching helpers."}
	}
	if err := ctx.Err(); err != nil {
		public := &proto.Error{Code: "cancelled", Message: "secret " + name + ": sentinel generation was cancelled", Hint: "Retry the approved secret operation when ready."}
		if err == context.Canceled || err == context.DeadlineExceeded {
			return errors.Join(public, err)
		}
		return public
	}
	return nil
}

func sentinelRune(r rune) bool {
	return r != 0 && !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r)
}

func compileSentinelShape(ctx context.Context, name string, shape sentinelShape) (*sentinelPlan, error) {
	if !validRegistryName(name) {
		return nil, &proto.Error{Code: "bad_secret", Message: "invalid secret name for sentinel generation", Hint: "Use the approved canonical environment name."}
	}
	if err := sentinelContext(ctx, name); err != nil {
		return nil, err
	}
	if !utf8.ValidString(shape.Prefix) || len(shape.Prefix) > proto.MaxFrame {
		return nil, sentinelFailure(name, "unsupported known-prefix representation")
	}
	prefixRunes := 0
	for _, r := range shape.Prefix {
		if prefixRunes%128 == 0 {
			if err := sentinelContext(ctx, name); err != nil {
				return nil, err
			}
		}
		prefixRunes++
		if !sentinelRune(r) {
			return nil, sentinelFailure(name, "unsupported known-prefix representation")
		}
	}
	plan := &sentinelPlan{prefix: shape.Prefix, bytes: len(shape.Prefix)}
	// Class tables belong to this compiled plan and remain immutable. Runs
	// keep their own counts while sharing complete source-backed alphabets.
	classes := make(map[string]sentinelGroup)
	for _, slot := range shape.Slots {
		if err := sentinelContext(ctx, name); err != nil {
			return nil, err
		}
		if slot.Count <= 0 || slot.Alphabet == "" || len(slot.Alphabet) > proto.MaxFrame {
			return nil, sentinelFailure(name, "unsupported token alphabet or run length")
		}
		group, known := classes[slot.Alphabet]
		if !known {
			if !utf8.ValidString(slot.Alphabet) {
				return nil, sentinelFailure(name, "unsupported token alphabet or run length")
			}
			group.allowed = make(map[rune]struct{})
			for _, r := range slot.Alphabet {
				if len(group.alphabet)%128 == 0 {
					if err := sentinelContext(ctx, name); err != nil {
						return nil, err
					}
				}
				width := utf8.RuneLen(r)
				if !sentinelRune(r) || group.width != 0 && group.width != width {
					return nil, sentinelFailure(name, "unsupported token alphabet or UTF8 width")
				}
				if _, exists := group.allowed[r]; exists {
					return nil, sentinelFailure(name, "token alphabet contains duplicate choices")
				}
				group.width = width
				group.allowed[r] = struct{}{}
				group.alphabet = append(group.alphabet, r)
			}
			classes[slot.Alphabet] = group
		}
		group.count = slot.Count
		// This is the existing transport representation ceiling, not a new
		// value/count cap. The registry still validates the complete state.
		if slot.Count > (proto.MaxFrame-plan.bytes)/group.width {
			return nil, sentinelFailure(name, "token shape exceeds the control representation limit")
		}
		plan.bytes += slot.Count * group.width
		plan.variable = plan.variable || len(group.alphabet) > 1
		plan.groups = append(plan.groups, group)
	}
	return plan, nil
}

func (p *sentinelPlan) matches(ctx context.Context, name string, value api.Secret) error {
	text := string(value)
	if len(text) != p.bytes || !utf8.ValidString(text) || !strings.HasPrefix(text, p.prefix) {
		return sentinelFailure(name, "value does not match its configured token shape")
	}
	text = text[len(p.prefix):]
	for _, group := range p.groups {
		for range group.count {
			if err := sentinelContext(ctx, name); err != nil {
				return err
			}
			r, width := utf8.DecodeRuneInString(text)
			if _, valid := group.allowed[r]; !valid || width != group.width {
				return sentinelFailure(name, "value does not match its configured token shape")
			}
			text = text[width:]
		}
	}
	return nil
}

// checkSentinelValue is also used before a rearm keeps an existing sentinel.
// It validates only representation, never binding or publication authority.
func checkSentinelValue(ctx context.Context, name string, value api.Secret, shape sentinelShape) error {
	plan, err := compileSentinelShape(ctx, name, shape)
	if err != nil {
		return err
	}
	return plan.matches(ctx, name, value)
}

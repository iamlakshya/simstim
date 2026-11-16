package client

import (
	"context"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// One JSON adapter feeds P23's same incremental validators. It does not
// implement another header, format or authority grammar.
func projectSandboxBind(ctx context.Context, p jsonValueProjection, span api.JSONSpan, retain bool) (api.SecretBind, error) {
	var fields [3]api.JSONSpan
	var out api.SecretBind
	err := p.Object(span, []string{"hosts", "header", "format"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	header := secretbind.NewCanonicalHeaderValidator(ctx)
	out.Header, err = projectBindingString(p, fields[1], header, retain)
	if err != nil {
		return out, err
	}
	if err := header.Finish(); err != nil {
		return out, err
	}
	format := secretbind.NewFormatValidator(ctx)
	out.Format, err = projectBindingString(p, fields[2], format, retain)
	if err != nil {
		return out, err
	}
	if err := format.Finish(); err != nil {
		return out, err
	}
	count := 0
	var previous api.JSONSpan
	err = p.Array(fields[0], func(host api.JSONSpan) error {
		if previous.End != 0 {
			order, err := p.CompareStrings(previous, host)
			if err != nil || order >= 0 {
				return badSandboxStateContext(ctx)
			}
		}
		previous = host
		authority := secretbind.NewCanonicalAuthorityValidator(ctx)
		if _, err := projectBindingString(p, host, authority, false); err != nil {
			return err
		}
		canonical, err := authority.Finish()
		if err != nil {
			return err
		}
		count++
		if retain {
			out.Hosts = append(out.Hosts, canonical)
		}
		return nil
	})
	if err != nil || count == 0 {
		if err != nil {
			return out, err
		}
		return out, badSandboxStateContext(ctx)
	}
	return out, ctx.Err()
}

func projectBindingString(p jsonValueProjection, span api.JSONSpan, validator io.Writer, retain bool) (string, error) {
	var kept strings.Builder
	var encoded [utf8.UTFMax]byte
	err := p.WalkString(span, func(r rune) error {
		n := utf8.EncodeRune(encoded[:], r)
		written, err := validator.Write(encoded[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		if retain {
			_, err = kept.Write(encoded[:n])
		}
		return err
	})
	return kept.String(), err
}

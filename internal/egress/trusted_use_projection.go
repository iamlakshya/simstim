package egress

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// TrustedUseProjectionV1 is a DATA reader over an already retained original
// JSON source. It creates no Source, native owner, value or permission.
type TrustedUseProjectionV1 interface {
	Object(api.JSONSpan, []string, func(api.JSONField) error) error
	Array(api.JSONSpan, func(api.JSONSpan) error) error
	Literal(api.JSONSpan, string) (bool, error)
	StringEqual(api.JSONSpan, string) (bool, error)
	WalkString(api.JSONSpan, func(rune) error) error
	WalkEncoded(api.JSONSpan, func([]byte) error) error
}

// TrustedUseProjectionSchemaV1 contains finite names from the ONE DTO schema.
type TrustedUseProjectionSchemaV1 struct{ use, binding []string }

func NewTrustedUseProjectionSchemaV1() TrustedUseProjectionSchemaV1 {
	return TrustedUseProjectionSchemaV1{use: api.ExactJSONFieldNames((*api.TrustedSecretUseV1)(nil)), binding: api.ExactJSONFieldNames((*api.SecretBind)(nil))}
}

// ValidateTrustedSecretNameProjectionV1 shares the canonical registry name
// grammar. A streamed name is data, never permission to resolve its value.
func ValidateTrustedSecretNameProjectionV1(p TrustedUseProjectionV1, span api.JSONSpan) error {
	if err := fdName(p, span); err != nil {
		return err
	}
	reserved, err := p.StringEqual(span, "SSH_AUTH_SOCK")
	if err != nil {
		return err
	}
	if reserved {
		return registryIntentError()
	}
	return nil
}

func trustedProjectionStringV1(p TrustedUseProjectionV1, span api.JSONSpan, out io.Writer, retain bool) (string, bool, error) {
	var kept strings.Builder
	var runeBytes [utf8.UTFMax]byte
	nonempty := false
	err := walkProjectedString(p, span, func(r rune) error {
		nonempty = true
		n := utf8.EncodeRune(runeBytes[:], r)
		if out != nil {
			written, err := out.Write(runeBytes[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if retain {
			_, err := kept.Write(runeBytes[:n])
			return err
		}
		return nil
	})
	return kept.String(), nonempty, err
}

// Project validates the SAME endpoint and secretbind kernels as the full
// role validator. retain=false keeps only bounded finite validator scratch;
// custom endpoint base paths, models and Basic usernames have no new cap.
func (schema TrustedUseProjectionSchemaV1) Project(ctx context.Context, p TrustedUseProjectionV1, span api.JSONSpan, retain bool) (api.TrustedSecretUseV1, error) {
	var fields [6]api.JSONSpan
	var result api.TrustedSecretUseV1
	unknown := false
	null, err := projectedNull(p, span)
	if err != nil {
		return result, err
	}
	if !null {
		err = p.Object(span, schema.use, func(field api.JSONField) error {
			if field.Known < 0 {
				unknown = true
				return nil
			}
			switch schema.use[field.Known] {
			case "v":
				fields[0] = field.Value
			case "use":
				fields[1] = field.Value
			case "mode":
				fields[2] = field.Value
			case "bind":
				fields[3] = field.Value
			case "model":
				fields[4] = field.Value
			case "endpoint":
				fields[5] = field.Value
			}
			return nil
		})
		if err != nil {
			return result, projectedOpaqueError(err)
		}
	}
	version, err := projectedNativeInt(p, fields[0])
	if err != nil {
		return result, err
	}
	for _, field := range []api.JSONSpan{fields[1], fields[2], fields[4], fields[5]} {
		if err := walkProjectedString(p, field, func(rune) error { return nil }); err != nil {
			return result, err
		}
	}
	if unknown {
		return result, errProjectedStateOpaque
	}
	use, err := fdStringEqual(p, fields[1], api.TrustedJudgeUseV1)
	if err != nil {
		return result, err
	}
	bound, err := fdStringEqual(p, fields[2], string(api.SecretModeBound))
	if err != nil {
		return result, err
	}
	if version != 1 || !use || !bound {
		return result, registryIntentError()
	}
	result.Version, result.Use, result.Mode = 1, api.TrustedJudgeUseV1, api.SecretModeBound
	var model bool
	result.Model, model, err = trustedProjectionStringV1(p, fields[4], nil, retain)
	if err != nil {
		return result, err
	}
	if !model {
		return result, registryIntentError()
	}
	endpoint := newTrustedEndpointStreamV1(ctx)
	result.Endpoint, _, err = trustedProjectionStringV1(p, fields[5], endpoint, retain)
	if err != nil {
		return result, err
	}
	authority, err := endpoint.Finish()
	if err != nil {
		return result, err
	}
	result.Bind, err = schema.bindingValue(ctx, p, fields[3], authority, retain)
	return result, err
}

func (schema TrustedUseProjectionSchemaV1) bindingValue(ctx context.Context, p TrustedUseProjectionV1, span api.JSONSpan, endpoint string, retain bool) (api.SecretBind, error) {
	var fields [3]api.JSONSpan
	var result api.SecretBind
	null, err := projectedNull(p, span)
	if err != nil {
		return result, err
	}
	if !null {
		err = p.Object(span, schema.binding, func(field api.JSONField) error {
			if field.Known < 0 {
				return errProjectedStateOpaque
			}
			switch schema.binding[field.Known] {
			case "hosts":
				fields[0] = field.Value
			case "header":
				fields[1] = field.Value
			case "format":
				fields[2] = field.Value
			}
			return nil
		})
		if err != nil {
			return result, projectedOpaqueError(err)
		}
	}
	header := secretbind.NewHeaderValidator(ctx)
	result.Header, _, err = trustedProjectionStringV1(p, fields[1], header, retain)
	if err != nil {
		return result, err
	}
	if err := header.Finish(); err != nil {
		return result, err
	}
	format := secretbind.NewFormatValidator(ctx)
	result.Format, _, err = trustedProjectionStringV1(p, fields[2], format, retain)
	if err != nil {
		return result, err
	}
	if err := format.Finish(); err != nil {
		return result, err
	}
	matched, count := false, 0
	null, err = projectedNull(p, fields[0])
	if err != nil {
		return result, err
	}
	if !null {
		err = p.Array(fields[0], func(host api.JSONSpan) error {
			validator := secretbind.NewAuthorityValidator(ctx)
			if _, _, err := trustedProjectionStringV1(p, host, validator, false); err != nil {
				return err
			}
			canonical, err := validator.Finish()
			if err != nil {
				return err
			}
			count++
			matched = matched || canonical == endpoint
			if retain {
				result.Hosts = append(result.Hosts, canonical)
			}
			return nil
		})
		if err != nil {
			return result, projectedOpaqueError(err)
		}
	}
	if count == 0 || !matched {
		return result, registryIntentError()
	}
	if retain {
		result.Header = http.CanonicalHeaderKey(result.Header)
		slices.Sort(result.Hosts)
		result.Hosts = slices.Compact(result.Hosts)
	}
	return result, ctx.Err()
}

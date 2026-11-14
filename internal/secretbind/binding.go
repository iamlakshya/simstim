// Package secretbind validates public binding metadata. It has no values,
// registry, network access, agent-name switch or policy authority.
package secretbind

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

// Canonical returns an owned binding to literal HTTPS authorities only.
// Matching it never replaces the actual network and address checks.
func Canonical(input api.SecretBind) (api.SecretBind, error) {
	header := NewHeaderValidator(context.Background())
	if _, err := header.Write([]byte(input.Header)); err != nil {
		return api.SecretBind{}, err
	}
	if err := header.Finish(); err != nil {
		return api.SecretBind{}, err
	}
	output := api.SecretBind{Header: http.CanonicalHeaderKey(input.Header), Format: input.Format}
	if _, _, err := ParseFormat(input.Format); err != nil {
		return api.SecretBind{}, err
	}
	if len(input.Hosts) == 0 {
		return api.SecretBind{}, errors.New("secret binding needs an exact HTTPS destination")
	}
	for _, raw := range input.Hosts {
		validator := NewAuthorityValidator(context.Background())
		if _, err := validator.Write([]byte(raw)); err != nil {
			return api.SecretBind{}, err
		}
		authority, err := validator.Finish()
		if err != nil {
			return api.SecretBind{}, err
		}
		output.Hosts = append(output.Hosts, authority)
	}
	slices.Sort(output.Hosts)
	output.Hosts = slices.Compact(output.Hosts)
	return output, nil
}

// ParseFormat recognises exactly the three formats of the secret schema.
// Basic's username is metadata and only its password is substituted.
func ParseFormat(format string) (kind, username string, err error) {
	validator := NewFormatValidator(context.Background())
	if _, err := validator.Write([]byte(format)); err != nil {
		return "", "", err
	}
	if err := validator.Finish(); err != nil {
		return "", "", err
	}
	switch validator.mode {
	case formatRaw:
		return "raw", "", nil
	case formatBearer:
		return "bearer", "", nil
	case formatBasic:
		return "basic", format[len(basicPrefix) : len(format)-len(basicSuffix)], nil
	}
	return "", "", errFormat
}

// parseBindingAuthority is the one bounded authority admission helper. It
// delegates syntax/normalisation to nettarget instead of another host parser.
func parseBindingAuthority(raw string) (nettarget.Target, error) {
	target, err := nettarget.Parse(raw)
	if err != nil || target.Wildcard || target.Path != "" || strings.Contains(raw, "/") {
		return nettarget.Target{}, errors.New("secret binding needs exact authorities without wildcards, URLs or paths")
	}
	if target.Port != 0 && target.Port != 443 {
		return nettarget.Target{}, errors.New("bound secrets require HTTPS on port 443")
	}
	if target.Prefix.IsValid() {
		if target.Prefix.Bits() != target.Prefix.Addr().BitLen() {
			return nettarget.Target{}, errors.New("secret binding cannot cover an address range")
		}
	}
	return target, nil
}

func materialiseBindingAuthority(target nettarget.Target) string {
	host := target.Host
	if target.Prefix.IsValid() {
		host = target.Prefix.Addr().String()
	}
	return net.JoinHostPort(host, "443")
}

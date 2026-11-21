package cachefs

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Domain binds host storage to one UID and canonical HOME scope and its checked
// HostID. A digest or a familiar pathname never substitutes for this owner.
// The zero value has no host authority. The tuple is deliberately comparable.
type Domain struct {
	Scope  string `json:"scope"`
	HostID string `json:"host_id"`
}

// Known reports whether both closed ownership fields have canonical values.
func (domain Domain) Known() bool {
	return domainHex(domain.Scope, 64) && domainHex(domain.HostID, 32)
}

// IsZero reports the legacy fixture or laptop value, never a host owner.
func (domain Domain) IsZero() bool { return domain.Scope == "" && domain.HostID == "" }

// Validate refuses an empty or malformed host domain with a typed Try hint.
func (domain Domain) Validate() error {
	if !domain.Known() {
		return domainError("the host store has no canonical scope and checked host identity")
	}
	return nil
}

// UnmarshalJSON accepts only the complete current owner schema. Refusal leaves
// the caller's original stored bytes untouched and grants no rewrite authority.
func (domain *Domain) UnmarshalJSON(raw []byte) error {
	value, err := decodeDomainContext(context.Background(), raw)
	if err == nil {
		*domain = value
	}
	return err
}

func decodeDomainContext(ctx context.Context, raw []byte) (Domain, error) {
	var domain Domain
	fields, err := domainObjectFields(ctx, raw, "scope", "host_id")
	if err != nil || len(fields) != 2 || json.Unmarshal(fields["scope"], &domain.Scope) != nil || json.Unmarshal(fields["host_id"], &domain.HostID) != nil {
		return domain, domainContextError(ctx, "the existing host domain is malformed or has unknown fields")
	}
	if err := domain.Validate(); err != nil {
		return domain, err
	}
	return domain, ctx.Err()
}

func domainHex(value string, size int) bool {
	return len(value) == size && strings.Trim(value, "0123456789abcdef") == ""
}

func domainError(message string) error {
	return &proto.Error{Code: "cache_domain", Message: message, Hint: "Restore the recorded host roots or use disjoint storage before retrying. Preserve unknown and other-domain files."}
}

func domainContextError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return domainError(message)
}

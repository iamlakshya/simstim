package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// imagePolicyReceipt records the scope of a real laptop verification. It is
// private pin metadata; it grants no host source, native root or boot action.
type imagePolicyReceipt struct {
	request  image.Request
	digest   string
	identity string
	policy   string
}

type imageCertificatePolicy struct {
	identity, issuer string
	root             []byte
	hash             string
}

func imagePolicyString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateImagePolicy(values *api.ImagePolicyValues) error {
	if values == nil {
		return nil
	}
	identity, issuer := imagePolicyString(values.Identity), imagePolicyString(values.Issuer)
	if (identity == "") != (issuer == "") {
		return &UserError{Msg: "sandbox.image_policy needs both identity and issuer", Hint: brand.Name + " config check", Code: "bad_config", Kind: KindUsage}
	}
	for _, text := range []string{identity, issuer, imagePolicyString(values.TrustedRoot)} {
		if len(text) > 4096 || strings.ContainsAny(text, "\x00\r\n") {
			return &UserError{Msg: "sandbox.image_policy contains an invalid value", Hint: brand.Name + " config check", Code: "bad_config", Kind: KindUsage}
		}
	}
	return nil
}

func readImageCertificatePolicy(ctx context.Context, values *api.ImagePolicyValues) (imageCertificatePolicy, error) {
	if err := ctx.Err(); err != nil {
		return imageCertificatePolicy{}, err
	}
	if err := validateImagePolicy(values); err != nil {
		return imageCertificatePolicy{}, err
	}
	if values == nil || imagePolicyString(values.Identity) == "" {
		return imageCertificatePolicy{}, nil
	}
	policy := imageCertificatePolicy{identity: imagePolicyString(values.Identity), issuer: imagePolicyString(values.Issuer), root: supply.SigstoreRoot()}
	if path := imagePolicyString(values.TrustedRoot); path != "" {
		if !filepath.IsAbs(path) {
			return imageCertificatePolicy{}, &UserError{Msg: "the image policy trusted root is not bound to its original configuration folder", Hint: "Select trusted_root through the original configuration operation.", Code: "image_signature", Kind: KindRefused}
		}
		data, err := ReadConfigFile(path)
		if err != nil {
			return imageCertificatePolicy{}, &UserError{Msg: "could not read the image policy trusted root: " + CleanLine(err.Error()), Hint: "Set trusted_root in [sandbox.image_policy] to a regular Sigstore root file on this laptop.", Code: "image_signature", Kind: KindRefused}
		}
		policy.root = data
	}
	policy.hash = supply.ImagePolicyHash(policy.identity, policy.issuer, imagePolicyString(values.TrustedRoot), policy.root)
	return policy, ctx.Err()
}

// verifyImagePolicy is called before a new pin is written and before any
// cached image status, host fetch, conversion or use. Its caller alone handles
// the original release-default and accepted saved-host source routes.
func verifyImagePolicy(ctx context.Context, request image.Request, registry *image.Registry, values *api.ImagePolicyValues) (imagePolicyReceipt, error) {
	if err := request.Validate(); err != nil {
		return imagePolicyReceipt{}, err
	}
	if request.SavedHost != nil {
		return imagePolicyReceipt{}, imagePolicyFailure(request, "a saved root has no OCI signature source")
	}
	policy, err := readImageCertificatePolicy(ctx, values)
	if err != nil {
		return imagePolicyReceipt{}, err
	}
	if policy.identity == "" {
		return imagePolicyReceipt{}, nil
	}
	lock, err := image.ReadLock(ctx, ConfigDir())
	if err != nil {
		return imagePolicyReceipt{}, imageClientError(err)
	}
	for _, pin := range lock.Images {
		if pin.HostID != "" || pin.Ref != request.Ref || pin.Digest != request.Digest {
			continue
		}
		if pin.SignedBy == policy.identity && pin.Policy == policy.hash {
			return imagePolicyReceipt{request: request, digest: request.Digest, identity: policy.identity, policy: policy.hash}, ctx.Err()
		}
		for _, platform := range pin.Platforms {
			if platform.Arch == request.Arch && platform.Digest == request.PlatformDigest && platform.SignedBy == policy.identity && platform.Policy == policy.hash {
				return imagePolicyReceipt{request: request, digest: request.PlatformDigest, identity: policy.identity, policy: policy.hash}, ctx.Err()
			}
		}
	}
	if registry == nil {
		registry, err = ImageRegistry(ctx, request.Ref)
		if err != nil {
			return imagePolicyReceipt{}, err
		}
	}
	ref, err := image.ParseReference(request.Ref)
	if err != nil {
		return imagePolicyReceipt{}, err
	}
	digests := []string{request.Digest}
	if request.PlatformDigest != request.Digest {
		digests = append(digests, request.PlatformDigest)
	}
	var failures []error
	for _, digest := range digests {
		signatures, discoveryErr := registry.Signatures(ctx, ref, digest)
		if discoveryErr != nil {
			failures = append(failures, discoveryErr)
		}
		for _, signature := range signatures {
			if err := verifyImageSignature(ctx, signature.Bundle, signature.Payload, digest, policy.identity, policy.issuer, policy.root); err != nil {
				if ctx.Err() != nil {
					return imagePolicyReceipt{}, ctx.Err()
				}
				var user *UserError
				if errors.As(err, &user) {
					return imagePolicyReceipt{}, err
				}
				failures = append(failures, err)
				continue
			}
			return imagePolicyReceipt{request: request, digest: digest, identity: policy.identity, policy: policy.hash}, ctx.Err()
		}
		if ctx.Err() != nil {
			return imagePolicyReceipt{}, ctx.Err()
		}
	}
	detail := "has no signature that the image policy accepts"
	if len(failures) > 0 {
		message := CleanLine(errors.Join(failures...).Error())
		if len(message) > 2048 {
			message = message[:2048]
		}
		detail += ": " + message
	}
	failure := imagePolicyFailure(request, detail)
	if len(failures) > 0 {
		failure.Hint = "Check the signature identity and issuer, then update the client or trusted_root in [sandbox.image_policy] if the signing key is newer."
	}
	return imagePolicyReceipt{}, failure
}

func imagePolicyFailure(request image.Request, detail string) *UserError {
	name := CleanLine(request.Ref)
	if ref, err := image.ParseReference(request.Ref); err == nil {
		name = registryPinnedReference(ref, request.Digest)
	}
	return &UserError{Msg: fmt.Sprintf("%s %s", name, detail), Hint: "Sign the pinned image with the identity and issuer in [sandbox.image_policy], or review that policy in config.toml.", Code: "image_signature", Kind: KindRefused}
}

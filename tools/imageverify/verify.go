package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// verifyImageSignature checks one registry bundle entirely offline. For a
// legacy simple-signing layer, payload is the exact signed layer bytes. A new
// bundle has no separate payload and must bind the expected artifact digest.
func verifyImageSignature(ctx context.Context, data, payload []byte, digest, identity, issuer string, trustedRoot []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 4<<20 || len(payload) > 4<<20 || len(trustedRoot) == 0 || len(trustedRoot) > 4<<20 || len(digest) != 71 || digest[:7] != "sha256:" || !validSHA256(digest[7:]) || identity == "" || issuer == "" {
		return fmt.Errorf("invalid image signature verification input")
	}
	material, err := root.NewTrustedRootFromJSON(trustedRoot)
	if err != nil {
		return fmt.Errorf("read Sigstore trusted root: %w", err)
	}
	verifier, err := verify.NewVerifier(material, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return fmt.Errorf("prepare offline Sigstore verifier: %w", err)
	}
	certificate, err := verify.NewShortCertificateIdentity(issuer, "", identity, "")
	if err != nil {
		return fmt.Errorf("read image certificate policy: %w", err)
	}
	var signed bundle.Bundle
	if err := signed.UnmarshalJSON(data); err != nil {
		return fmt.Errorf("read image signature bundle: %w", err)
	}
	expected, err := hex.DecodeString(digest[7:])
	if err != nil {
		return err
	}
	artifact := verify.WithArtifactDigest("sha256", expected)
	if len(payload) > 0 {
		if err := checkImageSignaturePayload(payload, digest); err != nil {
			return err
		}
		artifact = verify.WithArtifact(bytes.NewReader(payload))
	}
	if _, err := verifier.Verify(&signed, verify.NewPolicy(artifact, verify.WithCertificateIdentity(certificate))); err != nil {
		return fmt.Errorf("verify image signature: %w", err)
	}
	return ctx.Err()
}

func checkImageSignaturePayload(payload []byte, digest string) error {
	var claims struct {
		Critical struct {
			Type  string `json:"type"`
			Image struct {
				Digest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("read signed image claims: %w", err)
	}
	if claims.Critical.Type != "cosign container image signature" || claims.Critical.Image.Digest != digest {
		return fmt.Errorf("the signed image payload does not name the pinned digest")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

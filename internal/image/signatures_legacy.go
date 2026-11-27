package image

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
)

func (r *Registry) legacyImageSignature(ctx context.Context, ref Reference, layer signatureDescriptor) (Signature, error) {
	if layer.MediaType != "application/vnd.dev.cosign.simplesigning.v1+json" {
		return Signature{}, badLayer("unsupported legacy image signature media type")
	}
	payload, err := r.signatureBlob(ctx, ref, layer)
	if err != nil {
		return Signature{}, err
	}
	annotations := layer.Annotations
	signature, err := base64.StdEncoding.DecodeString(annotations["dev.cosignproject.cosign/signature"])
	if err != nil || len(signature) == 0 {
		return Signature{}, badLayer("legacy image signature has no valid signature")
	}
	certificate, rest := pem.Decode([]byte(annotations["dev.sigstore.cosign/certificate"]))
	if certificate == nil || certificate.Type != "CERTIFICATE" || len(rest) != 0 {
		return Signature{}, badLayer("legacy image signature has no valid certificate")
	}
	if _, err := x509.ParseCertificate(certificate.Bytes); err != nil {
		return Signature{}, badLayer("legacy image signature has an invalid certificate")
	}
	var rekor struct {
		SignedEntryTimestamp []byte
		Payload              struct {
			Body           string `json:"body"`
			IntegratedTime int64  `json:"integratedTime"`
			LogIndex       int64  `json:"logIndex"`
			LogID          string `json:"logID"`
		}
	}
	if json.Unmarshal([]byte(annotations["dev.sigstore.cosign/bundle"]), &rekor) != nil || len(rekor.SignedEntryTimestamp) == 0 || rekor.Payload.IntegratedTime <= 0 || rekor.Payload.LogIndex < 0 {
		return Signature{}, badLayer("legacy image signature has no valid transparency entry")
	}
	logID, err := hex.DecodeString(rekor.Payload.LogID)
	if err != nil || len(logID) != sha256.Size {
		return Signature{}, badLayer("legacy image signature has an invalid transparency log ID")
	}
	body, err := base64.StdEncoding.DecodeString(rekor.Payload.Body)
	if err != nil || len(body) == 0 {
		return Signature{}, badLayer("legacy image signature has an invalid transparency body")
	}
	var kind struct {
		Kind    string `json:"kind"`
		Version string `json:"apiVersion"`
	}
	if json.Unmarshal(body, &kind) != nil || kind.Kind != "hashedrekord" || kind.Version != "0.0.1" {
		return Signature{}, badLayer("legacy image signature has an unsupported transparency entry")
	}
	sum := sha256.Sum256(payload)
	// Convert public legacy metadata to the Sigstore v0.1 bundle schema. The
	// offline verifier checks its signed entry, certificate and payload itself.
	document := map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle+json;version=0.1",
		"verificationMaterial": map[string]any{
			"x509CertificateChain": map[string]any{"certificates": []any{map[string]any{"rawBytes": certificate.Bytes}}},
			"tlogEntries": []any{map[string]any{
				"logIndex":          strconv.FormatInt(rekor.Payload.LogIndex, 10),
				"logId":             map[string]any{"keyId": logID},
				"kindVersion":       map[string]any{"kind": kind.Kind, "version": kind.Version},
				"integratedTime":    strconv.FormatInt(rekor.Payload.IntegratedTime, 10),
				"inclusionPromise":  map[string]any{"signedEntryTimestamp": rekor.SignedEntryTimestamp},
				"canonicalizedBody": body,
			}},
		},
		"messageSignature": map[string]any{
			"messageDigest": map[string]any{"algorithm": "SHA2_256", "digest": sum[:]},
			"signature":     signature,
		},
	}
	bundle, err := json.Marshal(document)
	if err != nil {
		return Signature{}, fmt.Errorf("encode legacy image signature: %w", err)
	}
	return Signature{Bundle: bundle, Payload: payload}, nil
}

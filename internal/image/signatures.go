package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SignatureBundleType is cosign's OCI 1.1 referring artefact media type.
const SignatureBundleType = "application/vnd.dev.sigstore.bundle.v0.3+json"

const maxImageSignatures = 32

// Signature contains digest-checked registry bytes. Payload is present only
// for a legacy simple-signing layer. Neither field is a verification result.
type Signature struct {
	Bundle  []byte
	Payload []byte
}

type signatureDescriptor struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations"`
}

type signatureDocument struct {
	SchemaVersion int                   `json:"schemaVersion"`
	MediaType     string                `json:"mediaType"`
	ArtifactType  string                `json:"artifactType"`
	Manifests     []signatureDescriptor `json:"manifests"`
	Layers        []signatureDescriptor `json:"layers"`
	Subject       signatureDescriptor   `json:"subject"`
}

// Signatures reads bounded OCI referrers, then the referrers tag schema, then
// cosign's legacy signature tag. It uses the original registry credentials;
// no host takes part in discovery or supplies a signature verification result.
func (r *Registry) Signatures(ctx context.Context, ref Reference, digest string) ([]Signature, error) {
	if !ValidDigest(digest) {
		return nil, badLayer("signature discovery needs a canonical digest")
	}
	var found []Signature
	var failures []error
	seen := map[string]bool{}
	attempts := 0
	for _, location := range []struct{ kind, id string }{
		{"referrers", digest},
		{"manifests", strings.ReplaceAll(digest, ":", "-")},
		{"manifests", strings.ReplaceAll(digest, ":", "-") + ".sig"},
	} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := r.signatureDocument(ctx, ref, location.kind, location.id)
		if err != nil {
			if signatureMissing(err) {
				continue
			}
			failures = append(failures, err)
			continue
		}
		var doc signatureDocument
		if json.Unmarshal(data, &doc) != nil || doc.SchemaVersion != 2 {
			failures = append(failures, badLayer("invalid image signature index"))
			continue
		}
		if strings.HasSuffix(location.id, ".sig") {
			if len(doc.Layers) > maxImageSignatures || doc.MediaType != "application/vnd.oci.image.manifest.v1+json" && doc.MediaType != "application/vnd.docker.distribution.manifest.v2+json" {
				failures = append(failures, badLayer("invalid legacy image signature manifest"))
				continue
			}
			for _, layer := range doc.Layers {
				attempts++
				if attempts > maxImageSignatures {
					return nil, tooLarge("image has more than 32 signatures")
				}
				signature, err := r.legacyImageSignature(ctx, ref, layer)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				found = append(found, signature)
			}
			continue
		}
		if doc.MediaType != "application/vnd.oci.image.index.v1+json" || len(doc.Manifests) > maxImageSignatures {
			failures = append(failures, badLayer("invalid or oversized image signature index"))
			continue
		}
		for _, descriptor := range doc.Manifests {
			if descriptor.ArtifactType != SignatureBundleType || seen[descriptor.Digest] {
				continue
			}
			attempts++
			if attempts > maxImageSignatures {
				return nil, tooLarge("image has more than 32 signatures")
			}
			seen[descriptor.Digest] = true
			signature, err := r.referringImageSignature(ctx, ref, digest, descriptor)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			found = append(found, signature)
		}
	}
	return found, errors.Join(failures...)
}

func (r *Registry) referringImageSignature(ctx context.Context, ref Reference, subject string, descriptor signatureDescriptor) (Signature, error) {
	if !ValidDigest(descriptor.Digest) || descriptor.Size <= 0 || descriptor.Size > MaxManifest || descriptor.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		return Signature{}, badLayer("invalid image signature descriptor")
	}
	doc, err := r.Manifest(ctx, ref, descriptor.Digest)
	if err != nil {
		return Signature{}, err
	}
	var manifest signatureDocument
	if int64(len(doc.Data)) != descriptor.Size || json.Unmarshal(doc.Data, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.MediaType != descriptor.MediaType || manifest.ArtifactType != SignatureBundleType || manifest.Subject.Digest != subject || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != SignatureBundleType {
		return Signature{}, badLayer("image signature artefact does not name its expected subject")
	}
	data, err := r.signatureBlob(ctx, ref, manifest.Layers[0])
	return Signature{Bundle: data}, err
}

func (r *Registry) signatureBlob(ctx context.Context, ref Reference, descriptor signatureDescriptor) ([]byte, error) {
	if !ValidDigest(descriptor.Digest) || descriptor.Size <= 0 || descriptor.Size > MaxManifest {
		return nil, badLayer("invalid image signature layer")
	}
	var out bytes.Buffer
	if err := r.Blob(ctx, ref, Layer{Digest: descriptor.Digest, Size: descriptor.Size, MediaType: descriptor.MediaType}, &out, nil); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (r *Registry) signatureDocument(ctx context.Context, ref Reference, kind, identifier string) ([]byte, error) {
	response, err := r.get(ctx, ref, kind, identifier)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxManifest+1))
	if err != nil {
		return nil, fmt.Errorf("read image signature index: %w", err)
	}
	if int64(len(data)) > MaxManifest {
		return nil, badLayer("image signature index exceeds 4 MiB")
	}
	if response.Header.Get("Link") != "" {
		return nil, tooLarge("image signature index needs more than one bounded page")
	}
	return data, ctx.Err()
}

type registryStatusError struct{ status int }

func (e *registryStatusError) Error() string {
	return fmt.Sprintf("registry refused image request (HTTP %d)", e.status)
}

func signatureMissing(err error) bool {
	var status *registryStatusError
	return errors.As(err, &status) && (status.status == http.StatusNotFound || status.status == http.StatusMethodNotAllowed)
}

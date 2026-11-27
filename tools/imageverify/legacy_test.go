package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
)

type legacyFixtureTransport func(*http.Request) (*http.Response, error)

func (f legacyFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestLegacyRegistrySignaturePassesOnlyItsActualSignedImage(t *testing.T) {
	// This isolated test uses ephemeral keys and no service or registry. Its
	// whole path includes production legacy discovery, conversion and verifier.
	virtual, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	identity, issuer := "release@example.com", "https://issuer.example"
	digest := "sha256:" + strings.Repeat("a", 64)
	payload := []byte(`{"critical":{"type":"cosign container image signature","image":{"docker-manifest-digest":"` + digest + `"}},"optional":null}`)
	entity, err := virtual.SignWithVersion(identity, issuer, payload, "v0.1")
	if err != nil {
		t.Fatal(err)
	}
	verification, err := entity.VerificationContent()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := entity.SignatureContent()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := entity.TlogEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("test transparency entry: %v", err)
	}
	entry := entries[0]
	logPayload := tlog.RekorPayload{Body: entry.Body(), IntegratedTime: entry.IntegratedTime().Unix(), LogIndex: entry.LogIndex(), LogID: hex.EncodeToString([]byte(entry.LogKeyID()))}
	promise, err := virtual.RekorSignPayload(logPayload)
	if err != nil {
		t.Fatal(err)
	}
	annotation, _ := json.Marshal(map[string]any{"SignedEntryTimestamp": promise, "Payload": logPayload})
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: verification.Certificate().Raw})
	sum := sha256.Sum256(payload)
	layerDigest := "sha256:" + hex.EncodeToString(sum[:])
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "layers": []any{map[string]any{"mediaType": "application/vnd.dev.cosign.simplesigning.v1+json", "digest": layerDigest, "size": len(payload), "annotations": map[string]string{"dev.cosignproject.cosign/signature": base64.StdEncoding.EncodeToString(signature.Signature()), "dev.sigstore.cosign/certificate": string(cert), "dev.sigstore.cosign/bundle": string(annotation)}}}})
	registry := &image.Registry{Client: &http.Client{Transport: legacyFixtureTransport(func(request *http.Request) (*http.Response, error) {
		var data []byte
		switch request.URL.Path {
		case "/v2/team/dev/manifests/sha256-" + digest[7:] + ".sig":
			data = manifest
		case "/v2/team/dev/blobs/" + layerDigest:
			data = payload
		default:
			return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}
	ref, err := image.ParseReference("registry.example/team/dev:1")
	if err != nil {
		t.Fatal(err)
	}
	found, err := registry.Signatures(context.Background(), ref, digest)
	if err != nil || len(found) != 1 {
		t.Fatalf("legacy discovery: %+v %v", found, err)
	}
	material, err := root.NewTrustedRoot(root.TrustedRootMediaType01, virtual.FulcioCertificateAuthorities(), virtual.CTLogs(), virtual.TimestampingAuthorities(), virtual.RekorLogs())
	if err != nil {
		t.Fatal(err)
	}
	trustedRoot, err := material.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyImageSignature(context.Background(), found[0].Bundle, found[0].Payload, digest, identity, issuer, trustedRoot); err != nil {
		t.Fatal(err)
	}
	if err := verifyImageSignature(context.Background(), found[0].Bundle, found[0].Payload, "sha256:"+strings.Repeat("b", 64), identity, issuer, trustedRoot); err == nil {
		t.Fatal("legacy signature accepted another image")
	}
	if err := verifyImageSignature(context.Background(), found[0].Bundle, found[0].Payload, digest, "other@example.com", issuer, trustedRoot); err == nil {
		t.Fatal("legacy signature accepted another identity")
	}
}

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestImagePolicyReferenceUsesActualSchemaComments(t *testing.T) {
	fields, err := imagePolicyConfigFields(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sandbox.image_policy.identity": "exact cosign certificate identity", "sandbox.image_policy.issuer": "exact OIDC issuer", "sandbox.image_policy.trusted_root": "on the laptop"}
	if len(fields) != len(want) {
		t.Fatalf("documented %d image policy fields", len(fields))
	}
	for _, field := range fields {
		phrase, ok := want[field.key]
		if !ok || field.typ != "string" || !strings.Contains(field.doc, phrase) {
			t.Fatalf("reference does not follow its schema: %+v", field)
		}
	}
}

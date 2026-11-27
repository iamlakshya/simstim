package cli

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestEditorSSHAliasLookupHasNoPrefixOrFuzzyFallback(t *testing.T) {
	rows := []client.Session{{ID: "abcd2345", Name: "auth-fix"}, {ID: "efgh2345", Name: "other"}}
	for _, reference := range []string{"auth-fix", "abcd2345"} {
		row, err := findEditorSessionV1(rows, reference, "box")
		if err != nil || row.ID != "abcd2345" {
			t.Fatal(row, err)
		}
	}
	for _, reference := range []string{"abcd", "auth", "AUTH-FIX", "auth-fix "} {
		if row, err := findEditorSessionV1(rows, reference, "box"); err == nil || row != nil {
			t.Fatal("inexact alias selected a session", reference)
		}
	}
	ambiguous := append(rows, client.Session{ID: "ijkl2345", Name: "auth-fix"})
	if row, err := findEditorSessionV1(ambiguous, "auth-fix", "box"); err == nil || row != nil {
		t.Fatal("an ambiguous alias selected a session")
	}
}

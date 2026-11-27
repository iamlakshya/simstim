package client

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestHostSelectionRequestBindingKeepsTheSameRequestAndTarget(t *testing.T) {
	hostSelectionConfig(t, "[profiles.review.sandbox]\nhost = ['box', 'other']\n")
	selection, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: t.TempDir(), Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	r := NewGrantRequest(false, false)
	ctx, err := selection.BindNewRequest(t.Context(), r, "box")
	if err != nil || grantRequestOf(ctx) != r || r.hostSelection != selection || r.selectedHost != "box" {
		t.Fatal("binding replaced the original request or selection", err)
	}
	if _, err := selection.BindNewRequest(ctx, r, "other"); err == nil {
		t.Fatal("a rule list retargeted an existing request")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := selection.Check(t.Context()); err != nil {
		t.Fatal("closing one failed attempt closed the outer selection source", err)
	}
	next := NewGrantRequest(false, false)
	defer next.Close()
	if _, err := selection.BindNewRequest(t.Context(), next, "other"); err != nil {
		t.Fatal("the next target could not get its own fresh request", err)
	}
}

func TestHostSelectionRequestBindingRefusesPurposeAndAnswerDonation(t *testing.T) {
	for _, kind := range []string{"purpose", "answer", "other context", "closed"} {
		t.Run(kind, func(t *testing.T) {
			hostSelectionConfig(t, "")
			selection, err := PrepareHostSelection(t.Context(), "box", NewInputOptions{})
			if err != nil {
				t.Fatal(err)
			}
			closeHostSelection(t, selection)
			r := NewGrantRequest(false, false)
			ctx := t.Context()
			switch kind {
			case "purpose":
				r = NewReviewGrantRequest()
			case "answer":
				r.confirmed = map[grantScope]bool{{kind: OutboundSecret, name: "TOKEN"}: true}
			case "other context":
				ctx = WithGrantRequest(ctx, NewGrantRequest(false, false))
			case "closed":
				if err := selection.Close(); err != nil {
					t.Fatal(err)
				}
			}
			defer r.Close()
			if _, err := selection.BindNewRequest(ctx, r, "box"); err == nil {
				t.Fatal("an unrelated purpose, answer, context or closed source acquired selection provenance")
			}
		})
	}
}

func TestHostSelectionPublicationNeedsItsActualConnectionAndLiveSource(t *testing.T) {
	hostSelectionConfig(t, "")
	selection, err := PrepareHostSelection(t.Context(), "box", NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	r := NewGrantRequest(false, false)
	defer r.Close()
	ctx, err := selection.BindNewRequest(t.Context(), r, "box")
	if err != nil {
		t.Fatal(err)
	}
	host, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	conn := finishGeneratedTransportFixture(t, host, strings.Repeat("a", 32), "facts")
	if err := checkHostSelectionPublicationV1(ctx, conn); err != nil {
		t.Fatal("the original actual connection/source did not pass the extra selection check", err)
	}
	for _, forged := range []*Conn{nil, {Host: host}, {Host: Host{Name: "other"}}} {
		if err := checkHostSelectionPublicationV1(ctx, forged); err == nil {
			t.Fatal("a row or other target replaced the actual accepted connection")
		}
	}
	if err := os.WriteFile(settingsPath(), []byte("# replacement settings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkHostSelectionPublicationV1(ctx, conn); err == nil {
		t.Fatal("a replaced original settings source still passed publication")
	}
	if err := checkHostSelectionPublicationV1(context.Background(), nil); err != nil {
		t.Fatal("the extra hook changed existing callers with no selection", err)
	}
}

func TestHostSelectionAutoCannotBindFromMetadataAlone(t *testing.T) {
	candidate := placementCandidateFixture(t, nil, nil)
	selection, err := PrepareHostSelection(t.Context(), "auto", NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	r := NewGrantRequest(false, false)
	defer r.Close()
	if _, err := selection.BindNewRequest(t.Context(), r, candidate.host.Name); err == nil {
		t.Fatal("automatic metadata became a named-host permission")
	}
	if _, err := selection.bindSelectedAutoNewRequestV1(t.Context(), r, &autoPlacementCandidate{host: candidate.host, need: candidate.need}); err == nil {
		t.Fatal("a candidate row replaced its original facts observation")
	}
	ctx, err := selection.bindSelectedAutoNewRequestV1(t.Context(), r, candidate)
	if err != nil || grantRequestOf(ctx) != r || r.selectedHost != candidate.host.Name || r.hostSelection != selection {
		t.Fatal("the actual checked pool candidate did not retain the original request", err)
	}
	if r.newSecrets != nil || r.newIntent != nil || len(r.confirmed) != 0 || len(r.recorded) != 0 {
		t.Fatal("selection provenance created values, an approval or a grant record")
	}
}

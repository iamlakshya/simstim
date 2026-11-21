package client

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/imageverify"
)

func TestImageVerifierOutputOverflowCancelsOriginalProcessContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := imageVerifierOutput{limit: imageverify.MaxResponse, cancel: cancel}
	if _, err := output.Write([]byte(strings.Repeat("x", imageverify.MaxResponse))); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil || ctx.Err() == nil || !output.overflow {
		t.Fatal("overflow left the verifier running")
	}
	if len(output.data) != imageverify.MaxResponse {
		t.Fatal("overflow stored unbounded verifier bytes")
	}
}

func TestCancelledImageVerifierNeverOpensItsArtifactStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if stage, err := prepareImageVerifier(ctx); err != context.Canceled || stage != nil {
		t.Fatalf("cancelled verifier prepared a stage: %v %v", stage, err)
	}
}

func TestImageVerifierDiagnosticKeepsOneClientHint(t *testing.T) {
	message := imageVerifierDiagnostic([]byte("Error: the signature does not match\n\nTry: check the image policy and signature.\n"))
	if message != "the signature does not match" || strings.Contains(message, "Try:") {
		t.Fatalf("nested helper hint: %q", message)
	}
}

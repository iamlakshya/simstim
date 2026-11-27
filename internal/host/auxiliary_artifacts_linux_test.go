//go:build linux

package host

import (
	"context"
	"testing"
)

func TestAuxiliaryArtifactMetadataCannotIssueNativeCapture(t *testing.T) {
	for _, vacancy := range []*auxiliaryVacancyV1{nil, {}, {operation: &SessionOperation{id: "primary2"}}} {
		artifacts, err := captureAuxiliaryArtifactsV1(context.Background(), vacancy)
		if err == nil || artifacts != nil {
			t.Fatal("metadata vacancy issued auxiliary native artifacts")
		}
	}
	if err := (&auxiliaryArtifactsV1{}).CheckBeforeActivation(context.Background()); err == nil {
		t.Fatal("empty artifact record became a native boot source")
	}
}

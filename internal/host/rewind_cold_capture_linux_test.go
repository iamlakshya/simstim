//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRewindCannotDropOrPromoteUnprovedPartialColdV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "original-partial-artifact-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Actual temporary resources test retention only. These copied native
	// fields must fail; they do not stand in for a genuine stopped producer.
	operation := &SessionOperation{}
	stopped := &StoppedSessionV1{}
	artifacts := &auxiliaryArtifactsV1{disk: file}
	vacancy := &auxiliaryVacancyV1{operation: operation, stopped: stopped}
	partial := &ColdResumeV1{operation: operation, vacancy: vacancy, artifacts: artifacts}
	originalFailure := errors.New("original artifact creation cleanup is unresolved")
	preparation := &bridgeRewindPreparationV1{operation: operation, stopped: stopped, coldCapture: partial, coldCaptureErr: originalFailure}
	for range 2 {
		if err := preparation.captureLocalColdForRewindV1(context.Background()); err == nil {
			t.Fatal("copied stopped fields issued a complete cold Source")
		}
		if preparation.cold != nil || preparation.coldCapture != partial || partial.vacancy != vacancy || partial.artifacts != artifacts || preparation.coldCaptureErr != originalFailure {
			t.Fatal("a failed retry dropped or promoted actual partial resources")
		}
		if _, err := file.Stat(); err != nil {
			t.Fatal("failed source validation closed the retained original artifact", err)
		}
	}
}

func TestRewindUnboundPartialCannotReleaseOperationV1(t *testing.T) {
	operation := &SessionOperation{}
	preparation := &bridgeRewindPreparationV1{operation: operation, coldCapture: &ColdResumeV1{operation: operation}}
	if err := preparation.closeOwnedV1(); err == nil {
		t.Fatal("unsettled actual cold capture allowed operation release")
	}
	if preparation.coldCapture == nil || preparation.operation != operation {
		t.Fatal("refused cleanup dropped its same partial or operation")
	}
}

//go:build linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"sync"
	"testing"
)

func TestNativeResumeInspectionCannotOriginateFromStatusOrMode(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {id: "primary2"}} {
		warm, cold, err := InspectResumeForOperationV1(context.Background(), operation)
		if err == nil || warm != nil || cold != nil {
			t.Fatal("data-only operation issued a native resume variant")
		}
	}
	for _, warm := range []*WarmResumeV1{nil, {}, {owner: &StoppedSessionV1{source: Manifest{ID: "primary2"}}}} {
		if err := warm.Check(context.Background(), &SessionOperation{id: "primary2"}); err == nil {
			t.Fatal("data-only warm variant supplied native readiness")
		}
	}
	for _, cold := range []*ColdResumeV1{nil, {}, {operation: &SessionOperation{id: "primary2"}}} {
		if err := cold.Check(context.Background(), &SessionOperation{id: "primary2"}); err == nil {
			t.Fatal("data-only cold variant supplied native absence")
		}
	}
}

// A copied scope cannot renew its source by matching selection or status.
// Concurrent Close must also remain safe for partial failed captures.
func TestNativeResumePartialScopesCloseAndRemainInvalid(t *testing.T) {
	operation := &SessionOperation{id: "primary2"}
	for _, cold := range []*ColdResumeV1{{operation: operation}, {operation: operation, vacancy: &auxiliaryVacancyV1{operation: operation}, artifacts: &auxiliaryArtifactsV1{}}} {
		var jobs sync.WaitGroup
		for range 4 {
			jobs.Go(func() { _ = cold.Close() })
		}
		jobs.Wait()
		if err := cold.Check(context.Background(), operation); err == nil {
			t.Fatal("closed partial cold capture became native absence")
		}
		if err := cold.CheckManifest(context.Background(), operation, &Manifest{ID: operation.Primary()}); err == nil {
			t.Fatal("matching selection renewed a closed cold source")
		}
	}
	warm := &WarmResumeV1{owner: &StoppedSessionV1{operation: operation, source: Manifest{ID: operation.Primary()}}}
	if _, err := warm.Resume(context.Background(), operation, api.ResumeArgs{ID: operation.Primary()}); err == nil {
		t.Fatal("matching request data renewed a missing original warm controller")
	}
	if err := warm.Close(); err != nil {
		t.Fatal(err)
	}
	if err := warm.Check(context.Background(), operation); err == nil {
		t.Fatal("closed warm source became available")
	}
}

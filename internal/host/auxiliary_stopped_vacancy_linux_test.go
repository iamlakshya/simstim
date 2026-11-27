//go:build linux

package host

import (
	"context"
	"testing"
)

// These are refusal cases only. No test fabricates a positive native stop or
// treats a manifest, empty holder lock or saved status as a stopped producer.
func TestStoppedAuxiliaryVacancyRequiresOriginalNativeStopV1(t *testing.T) {
	for _, stopped := range []*StoppedSessionV1{nil, {}, {settled: true}, {settled: true, closed: true}} {
		owner, err := captureAuxiliaryVacancyFromStoppedV1(context.Background(), &SessionOperation{}, stopped)
		if err == nil || owner != nil {
			t.Fatal("a data-only stopped value supplied an auxiliary native vacancy")
		}
	}
}

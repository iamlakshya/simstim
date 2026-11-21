package supervisor

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestForkJobKeepsRejectedPartialUntilCleanupJoinsV1(t *testing.T) {
	// An unissued object must refuse native cleanup. The actual job must keep
	// this exact failed member even though it never entered the copy vector.
	rejected := &host.ForkDestinationV1{}
	job := &nativeForkJobV1{retained: []*host.ForkDestinationV1{rejected}}
	for range 2 {
		if err := job.closeDestinationsV1(); err == nil {
			t.Fatal("a rejected partial outside the copy vector was skipped")
		}
		if job.closed || len(job.retained) != 1 || job.retained[0] != rejected || len(job.destinations) != 0 {
			t.Fatal("unjoined cleanup lost the original partial or authorised copying")
		}
	}
}

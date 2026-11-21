//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

func TestPhysicalClosureKeyCoversNativeBootKernelAndDomain(t *testing.T) {
	domain := cachefs.Domain{Scope: "scope", HostID: "host"}
	var kernel unix.Utsname
	original, err := gvisorPhysicalDigestV1("all-runtime-companions", "held-C-and-Go", "boot", domain, kernel)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		runtime, bootstrap, boot string
		domain                   cachefs.Domain
		kernel                   unix.Utsname
	}{
		{"changed-companion", "held-C-and-Go", "boot", domain, kernel},
		{"all-runtime-companions", "changed-C-only", "boot", domain, kernel},
		{"all-runtime-companions", "held-C-and-Go", "new-boot", domain, kernel},
		{"all-runtime-companions", "held-C-and-Go", "boot", cachefs.Domain{Scope: "other", HostID: "host"}, kernel},
	}
	kernel.Release[0] = 1
	cases = append(cases, struct {
		runtime, bootstrap, boot string
		domain                   cachefs.Domain
		kernel                   unix.Utsname
	}{"all-runtime-companions", "held-C-and-Go", "boot", domain, kernel})
	for index, item := range cases {
		key, err := gvisorPhysicalDigestV1(item.runtime, item.bootstrap, item.boot, item.domain, item.kernel)
		if err != nil || key == original {
			t.Fatal("native closure key reused changed input", index, err)
		}
	}
}
func TestPhysicalProofCannotBePublishedWithoutOriginalProducer(t *testing.T) {
	probe := api.WallProbe{Driver: "gvisor", Level: "gvisor", OK: true, Facts: map[string]string{"native_closure_v1": "copied"}}
	if validateGvisorCachePublicationV1(context.Background(), []api.WallProbe{probe}) == nil {
		t.Fatal("copied OK row issued a native cache publication")
	}
	probe.OK = false
	if err := validateGvisorCachePublicationV1(context.Background(), []api.WallProbe{probe}); err != nil {
		t.Fatal(err)
	}
}

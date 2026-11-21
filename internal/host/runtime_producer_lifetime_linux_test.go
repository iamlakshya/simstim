//go:build linux

package host

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/publicationlease"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Positive receipts are produced by real status/delivery over actual checked
// files and native publication owners. No fixture inserts a receipt/map entry.
func runtimeProducerFixtureV1(t *testing.T) (*runtimeService, runtimecache.RuntimeRequest) {
	t.Helper()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("release manifests support amd64 and arm64")
	}
	setupIsolatedPrepareHost(t)
	manifest, defaults := installTestGvisorRuntimeV1(t, runtime.GOARCH)
	connection, cancel := context.WithCancel(t.Context())
	service := &runtimeService{}
	if err := service.bindConnectionV1(connection); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := service.close(); err != nil {
			t.Error(err)
		}
	})
	return service, runtimecache.RuntimeRequest{Manifest: manifest, ImageDefaults: &defaults}
}

func statusRuntimeReceiptV1(t *testing.T, service *runtimeService, request runtimecache.RuntimeRequest) *selectedRuntimeReceiptV1 {
	t.Helper()
	status, err := service.status(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range status.Items {
		if !item.Present {
			t.Fatal("actual installed fixture was not checked present")
		}
	}
	receipt, err := service.selectedRuntime(t.Context(), request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestRuntimeCompletedDeliveryRetainsOriginalConnectionReceiptV1(t *testing.T) {
	service, request := runtimeProducerFixtureV1(t)
	request.Transfer = strings.Repeat("e", 32)
	before := len(runtimeSlots)
	if err := service.registerFetch(t.Context(), request, 7); err != nil {
		t.Fatal(err)
	}
	job := service.jobs[request.Transfer]
	// Every requested original object is already genuinely installed. Fetch's
	// actual Has path completes without a remote request or an injected client.
	if _, err := service.fetchRegistered(request, 7); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(job.ctx.Err(), context.Canceled) || len(service.jobs) != 0 || len(runtimeSlots) != before {
		t.Fatal("successful delivery did not finish its actual job/slot")
	}
	receipt, err := service.selectedRuntime(t.Context(), request.Manifest)
	if err != nil {
		t.Fatal("normal job cancellation revoked its successful connection receipt", err)
	}
	if receipt.issuer != service || receipt.publication.ctx.Err() != nil {
		t.Fatal("delivery did not retain its original issuer/lifetime")
	}
	if actual := statusRuntimeReceiptV1(t, service, request); actual != receipt {
		t.Fatal("healthy actual status replaced an original receipt")
	}
	if _, err := receipt.defaultImageRequest(t.Context(), runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeStatusRetiresEndedOriginalBeforeFreshNativeReceiptV1(t *testing.T) {
	service, request := runtimeProducerFixtureV1(t)
	original := statusRuntimeReceiptV1(t, service, request)
	original.publication.cancel(errors.New("original native publication ended"))
	if _, err := service.selectedRuntime(t.Context(), request.Manifest); err == nil {
		t.Fatal("lookup renewed an ended original receipt")
	}
	fresh := statusRuntimeReceiptV1(t, service, request)
	if fresh == original || !original.closed || original.publication != nil || fresh.issuer != service {
		t.Fatal("fresh status did not retire the SAME ended owner before construction")
	}
	if _, err := original.inventory(t.Context()); err == nil {
		t.Fatal("retired original became usable after replacement")
	}
}

func TestRuntimeEndedOriginalCleanupFenceRefusesReplacementV1(t *testing.T) {
	service, request := runtimeProducerFixtureV1(t)
	original := statusRuntimeReceiptV1(t, service, request)
	loan, err := original.publication.operation.fence.RetainForHelperV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loan.Close() })
	original.publication.cancel(errors.New("original native publication ended"))
	if _, err := service.status(t.Context(), request); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("fresh producer bypassed an original live helper fence", err)
	}
	key, err := selectedRuntimeKey(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if service.selections[key] != original || service.selectionPending != original || !original.closed || original.publication == nil {
		t.Fatal("failed cleanup lost/replaced its exact original object")
	}
	other := request
	other.Manifest = cloneSelectedRuntime(request.Manifest)
	other.Manifest.Runtime = "different-runtime"
	if _, err := service.status(t.Context(), other); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("a different manifest bypassed failed original cleanup", err)
	}
	if err := loan.Close(); err != nil {
		t.Fatal(err)
	}
	if fresh := statusRuntimeReceiptV1(t, service, request); fresh == original {
		t.Fatal("retry revived an original closed receipt")
	}
}

func TestRuntimeCancellationAndServiceCloseCannotRenewProducerV1(t *testing.T) {
	service, request := runtimeProducerFixtureV1(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.status(canceled, request); !errors.Is(err, context.Canceled) || len(service.selections) != 0 {
		t.Fatal("canceled request produced a receipt", err)
	}
	original := statusRuntimeReceiptV1(t, service, request)
	if err := service.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.status(t.Context(), request); err == nil {
		t.Fatal("closed service admitted a new producer")
	}
	if _, err := original.inventory(t.Context()); err == nil {
		t.Fatal("service close left its original selected receipt usable")
	}
	if err := service.bindConnectionV1(t.Context()); err == nil {
		t.Fatal("closed service replaced its accepted lifetime")
	}
}

func TestRuntimeOriginalManifestClonePreservesEmptyVectorsV1(t *testing.T) {
	manifest := supply.Manifest{Items: []supply.Item{}, Advisories: []supply.Advisory{}}
	cloned := cloneSelectedRuntime(manifest)
	if !reflect.DeepEqual(manifest, cloned) {
		t.Fatal("clone changed the exact original full-manifest shape")
	}
	empty := []supply.Item{}
	if got := cloneImageDefaults(&empty); got == nil || *got == nil {
		t.Fatal("explicit empty defaults became unknown")
	}
}

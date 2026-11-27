package onboard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func TestUnsavedRetainsRawResultWithoutOpeningOrDestroying(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "cancelled"}[cancelled], func(t *testing.T) {
			base := newFake(t, "cleanup-authority")
			p := &returnedPinCloseFailure{fakeProvider: base}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if cancelled {
				stop()
			}
			marker := errors.New("actual binding or durable-save failure")
			h := client.Host{Name: "box", Machine: &client.Machine{Provider: p.Name(), ID: "reported-only", Source: &client.MachineSourceProvenance{Version: 1, Kind: "typed-data-is-not-authority"}}}
			err := unsaved(ctx, p, provider.Request{Name: h.Name}, h, marker)
			if !errors.Is(err, marker) || !strings.Contains(err.Error(), "reported-only") || !strings.Contains(err.Error(), "may remain") {
				t.Fatalf("lost retained outcome/cause: %v", err)
			}
			if p.opens != 0 || len(p.m.Calls()) != 0 {
				t.Fatal("raw returned fields or cancellation donated native cleanup authority")
			}
		})
	}
}

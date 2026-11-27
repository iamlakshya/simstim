package onboard

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type returnedPinCloseFailure struct {
	fakeProvider
	actual client.Host
	marker error
	cancel context.CancelFunc
	opens  int
}

func (p *returnedPinCloseFailure) Create(_ context.Context, req provider.Request) (_ client.Host, returned error) {
	p.actual = client.Host{Name: req.Name, Machine: &client.Machine{Provider: p.Name(), ID: "actual-returned-resource", Source: &client.MachineSourceProvenance{Version: 99, Kind: "unknown-result-data"}}}
	// Same returned-Host-plus-deferred-pin-close-error shape as the actual
	// P114 producer. This caller test performs no provider/native inspection.
	defer func() { returned = errors.Join(returned, p.marker) }()
	if p.cancel != nil {
		p.cancel()
	}
	return p.actual, nil
}

func (p *returnedPinCloseFailure) Open(client.Host) (client.Lifecycle, error) {
	p.opens++
	return p.m, nil
}

func TestActualCreateRetainsReturnedHostWhenProducerPinCloseFails(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "live caller", true: "cancelled caller"}[cancel], func(t *testing.T) {
			base := newFake(t, "returned-pin-close")
			marker := errors.New("actual provider pin close failed after returning the resource")
			p := &returnedPinCloseFailure{fakeProvider: base, marker: marker}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if cancel {
				p.cancel = stop
			}
			h, err := Create(ctx, p, provider.Request{Name: "box", Out: io.Discard})
			if !errors.Is(err, marker) || h.Machine != p.actual.Machine || !reflect.DeepEqual(h, p.actual) {
				t.Fatalf("actual returned Host/cause was discarded or rebuilt: %+v, %v", h, err)
			}
			var user *client.UserError
			if !errors.As(err, &user) || !strings.Contains(user.Msg, "actual-returned-resource") || !strings.Contains(user.Msg, "may remain") {
				t.Fatalf("partial resource outcome was not reported: %v", err)
			}
			if p.opens != 0 || len(p.m.Calls()) != 0 {
				t.Fatal("nonempty data guessed Open/Destroy cleanup authority")
			}
			if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 || h.Machine.Created != "" {
				t.Fatalf("partial/error result was bound or saved: %+v, %v", hosts, err)
			}
		})
	}
}

func TestCreationRequestKeepsActualCanonicalByteFields(t *testing.T) {
	p := newFake(t, "request-bytes")
	r := provider.Request{Name: "box", CPUs: 3, Memory: 123456789, Disk: 987654321, Image: "selected-image", IdleStop: "never"}
	got := CreationRequest(p, r)
	if got.Name != r.Name || got.Provider != p.Name() || got.CPUs != r.CPUs || got.Memory != r.Memory || got.Disk != r.Disk || got.Image != r.Image || got.IdleStop != r.IdleStop {
		t.Fatal("protected request used stale GiB DTO fields or changed actual selected bytes")
	}
}

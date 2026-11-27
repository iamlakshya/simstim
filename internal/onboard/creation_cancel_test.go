package onboard

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

type creationReplyProvider struct {
	name   string
	create func(context.Context, provider.Request) (client.Host, error)
	open   func(client.Host) (client.Lifecycle, error)
}

func (p creationReplyProvider) Name() string { return p.name }
func (p creationReplyProvider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	return p.create(ctx, req)
}
func (p creationReplyProvider) Open(h client.Host) (client.Lifecycle, error) {
	return p.open(h)
}

func TestCreationErrorCannotDestroyExternalReplacementAfterAbsentObservation(t *testing.T) {
	base := newFake(t, Docker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	absentObserved, foreignCreated := false, false
	p := creationReplyProvider{name: Docker, open: func(client.Host) (client.Lifecycle, error) { return base.m, nil }}
	p.create = func(ctx context.Context, req provider.Request) (client.Host, error) {
		// This models the provider's own absent-name check, followed by an
		// external creator winning before this attempt can publish anything.
		state, err := base.m.Status(ctx)
		if err != nil || state != client.MachineMissing {
			t.Fatal("fixture did not observe actual absence", err)
		}
		absentObserved = true
		base.m.SetState(client.MachineRunning)
		foreignCreated = true
		cancel()
		return client.Host{}, ctx.Err()
	}
	_, err := Create(ctx, p, providerRequest("box"))
	var userError *client.UserError
	if !absentObserved || !foreignCreated || !errors.As(err, &userError) || userError.Kind != client.KindCancelled || !strings.Contains(userError.Msg, "may remain") {
		t.Fatal("the actual partial outcome was not reported", absentObserved, foreignCreated, err)
	}
	for _, call := range base.m.Calls() {
		if call == "destroy" {
			t.Fatal("preflight absence authorised deleting another creator's machine")
		}
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("an unknown provider result saved a host")
	}
}

func providerRequest(name string) provider.Request {
	return provider.Request{Name: name, Out: io.Discard}
}

func TestCreationErrorAndFailedOpenNeverClaimDeletion(t *testing.T) {
	newFake(t, Docker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := 0
	p := creationReplyProvider{name: Docker,
		create: func(context.Context, provider.Request) (client.Host, error) {
			cancel()
			return client.Host{}, context.Canceled
		},
		open: func(client.Host) (client.Lifecycle, error) {
			opened++
			return nil, errors.New("cannot acquire a created-resource handle")
		}}
	_, err := Create(ctx, p, providerRequest("box"))
	if err == nil || opened != 0 || strings.Contains(err.Error(), "was deleted") || !strings.Contains(err.Error(), "may remain") {
		t.Fatal("unknown partial creation was acquired or falsely declared deleted", opened, err)
	}
}

func TestCreationValidatedSuccessStillCleansExactUnsavedResponse(t *testing.T) {
	p := newFake(t, Docker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.onCreate = cancel // The provider returns its real successful response.
	_, err := Create(ctx, p, providerRequest("box"))
	if err == nil || !strings.Contains(err.Error(), "was deleted again") {
		t.Fatal("validated successful response lost its owned unsaved cleanup", err)
	}
	destroyed := 0
	for _, call := range p.m.Calls() {
		if call == "destroy" {
			destroyed++
		}
	}
	if destroyed != 1 {
		t.Fatal("owned response cleanup was absent or repeated", destroyed, p.m.Calls())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("cancelled first publication saved the host")
	}
}

func TestCreationMalformedSuccessfulReplyCannotAuthoriseCleanup(t *testing.T) {
	for _, changed := range []string{"name", "provider", "id", "named-id"} {
		t.Run(changed, func(t *testing.T) {
			base := newFake(t, Docker)
			opened := 0
			p := creationReplyProvider{name: Docker, create: func(_ context.Context, req provider.Request) (client.Host, error) {
				h := client.Host{Name: req.Name, Machine: &client.Machine{Provider: Docker, ID: provider.MachineName(req.Name)}}
				switch changed {
				case "name":
					h.Name = "another"
				case "provider":
					h.Machine.Provider = Lima
				case "id":
					h.Machine.ID = ""
				case "named-id":
					h.Machine.ID = provider.MachineName("another")
				}
				return h, nil
			}, open: func(client.Host) (client.Lifecycle, error) {
				opened++
				return base.m, nil
			}}
			_, err := Create(t.Context(), p, providerRequest("box"))
			var userError *client.UserError
			if !errors.As(err, &userError) || userError.Code != "provider_identity" || opened != 0 {
				t.Fatal("foreign reply supplied cleanup or save authority", opened, err)
			}
		})
	}
}

var _ provider.Provider = creationReplyProvider{}
var _ client.Lifecycle = (*clienttest.Machine)(nil)

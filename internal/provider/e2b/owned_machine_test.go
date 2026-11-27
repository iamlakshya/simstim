package e2b

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func TestCreateMachineRequiresOriginalOwnerBeforeCredentialsOrNetwork(t *testing.T) {
	p, api, _ := testProvider(t)
	h, resource, err := p.CreateMachineV1(t.Context(), provider.Request{Name: "cloud"})
	if err == nil || resource != nil || h.Machine != nil || len(api.Calls()) != 0 {
		t.Fatalf("unowned creation = %v, %v, %v; calls %+v", h, resource, err, api.Calls())
	}
}

func TestOwnedMachineKeepsActualAPIKeyEndpointAndResource(t *testing.T) {
	p, api, envd := testProvider(t)
	t.Setenv("E2B_API_URL", p.APIURL)
	t.Setenv("E2B_SANDBOX_URL", p.SandboxURL)
	p.APIURL, p.SandboxURL = "", ""
	checks := 0
	h, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error { checks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resource.Close(); err != nil {
			t.Error(err)
		}
	}()
	if checks != 3 {
		t.Fatalf("creation checked %d times, want before auth, before create and after create", checks)
	}
	var foreign atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { foreign.Add(1); w.WriteHeader(http.StatusTeapot) }))
	t.Cleanup(other.Close)
	t.Setenv("E2B_API_KEY", "foreign-key")
	t.Setenv("E2B_API_URL", other.URL)
	t.Setenv("E2B_SANDBOX_URL", other.URL)
	t.Setenv("E2B_DOMAIN", "foreign.invalid")
	h.Machine.ID, h.Machine.TokenCommand = "foreign-resource", "exit 42"
	metadata := resource.Host()
	metadata.Machine.ID = "metadata-is-no-authority"
	lifecycle := resource.Lifecycle()
	if state, err := lifecycle.Status(t.Context()); err != nil || state != client.MachineRunning {
		t.Fatalf("status = %q, %v", state, err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	leaser, ok := lifecycle.(client.Leaser)
	if !ok {
		t.Fatal("owned lifecycle lost its lease contract")
	}
	if err := leaser.Renew(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	transport, err := lifecycle.Transport(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := transport.Run(t.Context(), "printf 'original transport'", nil)
	if err != nil || string(out) != "original transport" {
		t.Fatalf("transport = %q, %v", out, err)
	}
	if err := resource.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := resource.Rollback(t.Context()); err != nil {
		t.Fatalf("repeat rollback = %v", err)
	}
	if state, err := lifecycle.Status(t.Context()); err != nil || state != client.MachineMissing {
		t.Fatalf("rolled-back status = %q, %v", state, err)
	}
	if foreign.Load() != 0 || len(envd.Paths()) == 0 {
		t.Fatalf("foreign requests = %d; original envd requests = %v", foreign.Load(), envd.Paths())
	}
	kills := 0
	for _, call := range api.Calls() {
		if call.Method == http.MethodDelete {
			kills++
			if call.Path != "/sandboxes/"+testID {
				t.Fatalf("rollback used reconstructed resource %q", call.Path)
			}
		}
	}
	if kills != 1 {
		t.Fatalf("rollback calls = %d, want one", kills)
	}
}

func TestOwnedMachineCancellationAfterCreateRetainsActualRollback(t *testing.T) {
	p, api, _ := testProvider(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checks := 0
	h, resource, err := p.createOwnedMachine(ctx, provider.Request{Name: "cloud"}, func() error {
		checks++
		if checks == 3 {
			cancel()
		}
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || resource == nil || h.Machine == nil || h.Machine.ID != testID {
		t.Fatalf("cancelled creation = %+v, %v, %v", h, resource, err)
	}
	t.Setenv("E2B_API_KEY", "replacement-must-not-be-read")
	p.APIURL = "https://replacement.invalid"
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer stop()
	if err := resource.Rollback(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	calls := api.Calls()
	if len(calls) != 3 || calls[2].Method != http.MethodDelete || calls[2].Path != "/sandboxes/"+testID {
		t.Fatalf("cancel cleanup = %+v", calls)
	}
}

func TestOwnedMachineSavedLifecycleKeepsOriginalProviderOrigins(t *testing.T) {
	p, api, _ := testProvider(t)
	host, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resource.Close(); err != nil {
			t.Error(err)
		}
	}()
	host.Machine.Session = "work"
	if host.Machine.Endpoint != p.APIURL || host.Machine.TransportURL != p.SandboxURL {
		t.Fatal("the actual create did not retain its original provider origins")
	}
	var foreign atomic.Int32
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { foreign.Add(1); w.WriteHeader(http.StatusTeapot) }))
	t.Cleanup(replacement.Close)
	t.Setenv("E2B_API_URL", replacement.URL)
	t.Setenv("E2B_SANDBOX_URL", replacement.URL)
	savedProvider := Provider{HTTP: p.HTTP, CLIConfig: p.CLIConfig}
	lifecycle, err := savedProvider.Open(host)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := lifecycle.Status(t.Context()); err != nil || state != client.MachineRunning {
		t.Fatalf("saved status = %q, %v", state, err)
	}
	if foreign.Load() != 0 || len(api.Calls()) != 3 {
		t.Fatalf("saved lifecycle selected another provider: foreign=%d, original=%v", foreign.Load(), api.Calls())
	}
}

func TestOwnedMachineRechecksSourceBeforeCreate(t *testing.T) {
	p, api, _ := testProvider(t)
	changed := errors.New("original source changed")
	checks := 0
	_, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error {
		checks++
		if checks == 2 {
			return changed
		}
		return nil
	})
	if !errors.Is(err, changed) || resource != nil {
		t.Fatalf("source failure = %v, %v", resource, err)
	}
	calls := api.Calls()
	if len(calls) != 1 || calls[0].Path != "/templates" {
		t.Fatalf("creation continued after source changed: %+v", calls)
	}
}

func TestOwnedMachineRejectsForeignStatusAndResumeIdentity(t *testing.T) {
	for _, entryName := range []string{"get_running", "connect"} {
		t.Run(entryName, func(t *testing.T) {
			p, api, _ := testProvider(t)
			_, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resource.Close(); err != nil {
					t.Error(err)
				}
			}()
			api.all[entryName] = bytes.ReplaceAll(api.all[entryName], []byte(testID), []byte("foreign-resource"))
			if entryName == "get_running" {
				_, err = resource.Lifecycle().Status(t.Context())
			} else {
				err = resource.Lifecycle().Start(t.Context())
			}
			var user *client.UserError
			if !errors.As(err, &user) || user.Code != "machine_identity" {
				t.Fatalf("foreign identity = %v", err)
			}
		})
	}
}

func TestOwnedMachineCloseRequiresOwnedProcessWaitAndRevokesTransport(t *testing.T) {
	p, _, envd := testProvider(t)
	_, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	transport, err := resource.Lifecycle().Transport(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	process, err := transport.Start(ctx, "printf 'done'")
	if err != nil {
		t.Fatal(err)
	}
	_ = process.Stdin().Close()
	if _, err := io.Copy(io.Discard, process.Stdout()); err != nil {
		t.Fatal(err)
	}
	if err := resource.Close(); err == nil {
		t.Fatal("Close released credentials before the original process was joined")
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	before := len(envd.Paths())
	if _, err := transport.Run(t.Context(), "exit 99", nil); err == nil {
		t.Fatal("a transport started after its creation owner closed")
	}
	if len(envd.Paths()) != before {
		t.Fatal("a closed handle reached the provider")
	}
}

func TestOwnedMachineRedirectNeverForwardsAPIKeyToAnotherOrigin(t *testing.T) {
	var leaked atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaked.Add(1); w.WriteHeader(http.StatusOK) }))
	t.Cleanup(foreign.Close)
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/templates", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(original.Close)
	t.Setenv("E2B_API_KEY", testKey)
	p := Provider{APIURL: original.URL, CLIConfig: filepath.Join(t.TempDir(), "absent.json")}
	_, resource, err := p.createOwnedMachine(t.Context(), provider.Request{Name: "cloud"}, func() error { return nil })
	if err == nil || resource != nil || !strings.Contains(err.Error(), "original provider endpoint") || leaked.Load() != 0 {
		t.Fatalf("redirect = %v, %v; foreign requests %d", resource, err, leaked.Load())
	}
}

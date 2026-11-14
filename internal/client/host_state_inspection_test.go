package client

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHostCreationPreflightExistingViewHasNoWriteOrEscapingFDAuthority(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	folder := filepath.Join(StateDir(), "hosts", HostFileKey(request.Name))
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"removal.json": "private generated owner fixture", "removal.json.lock": ""} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var retained io.ReaderAt
	err = withHostCreationRemovalInspection(ctx, request.Name, func(view stateFileView) error {
		retained = view.ReaderAt
		if !view.Exists {
			t.Fatal("the existing fixture journal was lost")
		}
		got, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
		if err != nil || string(got) != "private generated owner fixture" {
			t.Fatalf("existing view changed bytes: %q, %v", got, err)
		}
		called := false
		if err := hostObservationAttempt(ctx, Host{Name: request.Name}, "removal.json", &called); err == nil || called {
			t.Fatal("the owner callback borrowed the private inspection write capability")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("inspection descriptor escaped its actual transaction: %v", err)
	}
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withHostCreationRemovalInspection(ctx, request.Name, func(stateFileView) error { called = true; return nil }); err == nil || called {
		t.Fatal("journal preflight ran after the provider phase began")
	}
}

func TestHostCreationPreflightDoesNotCreateMissingSentinel(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	folder := filepath.Join(StateDir(), "hosts", HostFileKey(request.Name))
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "removal.json"), []byte("private generated fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	called := false
	if err := withHostCreationRemovalInspection(ctx, request.Name, func(stateFileView) error { called = true; return nil }); err == nil || called {
		t.Fatal("a journal without its existing canonical sentinel was adopted")
	}
	if _, err := os.Lstat(filepath.Join(folder, "removal.json.lock")); !os.IsNotExist(err) {
		t.Fatalf("inspection created a new sentinel domain: %v", err)
	}
}

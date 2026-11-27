//go:build unix

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestImageCredentialFIFOIsRefusedBeforeItsBlockingOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	if err := unix.Mkfifo(filepath.Join(dir, "config.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := imageCredentials(ctx, "registry.example"); err == nil {
		t.Fatal("credential FIFO was read")
	}
}

func TestImageCredentialHelperCancellationJoinsItsOriginalProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credsStore":"waiting"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-waiting"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := imageCredentials(ctx, "registry.example"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential cancellation: %v", err)
	}
}

func TestImageCredentialHelperOverflowStopsItsActualOriginalProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credsStore":"overflow"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	program := "#!/bin/sh\nwhile :; do printf '" + strings.Repeat("x", 4096) + "'; done\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-overflow"), []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := imageCredentials(ctx, "registry.example"); err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Fatalf("credential overflow did not cancel and join its helper: %v", err)
	}
}

package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
)

func TestImageCredentialConfigRequiresItsOriginalRegularFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	path := filepath.Join(dir, "config.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := imageCredentials(t.Context(), "registry.example"); err == nil {
		t.Fatal("registry credential directory was read")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "actual.json")
	if err := os.WriteFile(target, []byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := imageCredentials(t.Context(), "registry.example"); err == nil {
		t.Fatal("registry credential symlink was followed")
	}
}

func TestImageCredentialOverflowCancelsTheOriginalChildContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output bytes.Buffer
	writer := imageBoundedBuffer{buffer: &output, remaining: image.MaxManifest, cancel: cancel}
	if _, err := writer.Write(make([]byte, image.MaxManifest)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err == nil || ctx.Err() == nil || !writer.overflow {
		t.Fatal("credential overflow left the original helper running")
	}
	if output.Len() != int(image.MaxManifest) {
		t.Fatal("credential overflow retained extra bytes")
	}
}

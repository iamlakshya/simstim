package image

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageDomainHome(t *testing.T) {
	t.Helper()
	isolateImageOwnership(t)
	for _, key := range []string{"XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		other := t.TempDir()
		t.Setenv(key, os.Getenv(key)+string(os.PathListSeparator)+other)
	}
}

func imageDomainFixture(scope, host string) cachefs.Domain {
	return cachefs.Domain{Scope: strings.Repeat(scope, 64), HostID: strings.Repeat(host, 32)}
}

func imageDomainGate(_ context.Context, publish func() error) error { return publish() }

func imageDomainPath(t *testing.T, leaf string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, leaf)
}

func imageDomainStore(t *testing.T, domain cachefs.Domain) *Store {
	t.Helper()
	store, err := OpenHost(t.Context(), imageDomainPath(t, "images"), domain, imageDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// This fixture copies a canonical locally converted descriptor. It performs
// no registry lookup, network request, shell command or guest execution.
func imageDomainPublish(t *testing.T, store *Store, letter string) (Request, Record) {
	t.Helper()
	input, checked := convertFixture(t)
	request := Request{Ref: "registry.example/team/base:stable", Digest: "sha256:" + strings.Repeat(letter, 64), PlatformDigest: "sha256:" + strings.Repeat(letter, 64), Arch: "amd64"}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	record, err := store.publish(t.Context(), request, nil, nil, func(_ context.Context, _ []Layer, _ []*os.File, out *os.File) error {
		_, err := io.Copy(out, io.NewSectionReader(input, 0, checked.Size))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return request, record
}

func requireImageDomainError(t *testing.T, err error) {
	t.Helper()
	var refused *proto.Error
	if !errors.As(err, &refused) || refused.Code != "cache_domain" || refused.Message == "" || refused.Hint == "" {
		t.Fatalf("expected canonical typed domain refusal and Try hint: %v", err)
	}
}

func imageDomainRead(t *testing.T, store *Store, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.root.Name(), filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func imageDomainReceiptBytes(t *testing.T, store *Store, name string, data []byte) {
	t.Helper()
	root, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := replaceReceipt(root, name, data); err != nil {
		t.Fatal(err)
	}
}

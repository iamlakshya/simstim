package image

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostExistingImageReaderDoesNotMarkUseOrCreateInfrastructure(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	request, record := imageDomainPublish(t, store, "c")
	request.RootHash, request.DataBlocks = record.RootHash, record.DataBlocks
	before := imageDomainRead(t, store, request.PlatformDigest[7:]+"/image.json")
	existing, err := OpenHostExisting(t.Context(), store.root.Name(), store.domain)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	file, lease, opened, err := existing.OpenImageExisting(t.Context(), request)
	if err != nil {
		t.Fatal("read-only VM asset open entered writable Cached", err)
	}
	if opened.RootHash != request.RootHash {
		t.Fatal("reader ignored exact expected root")
	}
	file.Close()
	lease.Close()
	if actual := imageDomainRead(t, store, request.PlatformDigest[7:]+"/image.json"); !bytes.Equal(actual, before) {
		t.Fatal("VM read-only open changed UsedAt/metadata")
	}
	request.RootHash = strings.Repeat("f", 64)
	if file, lease, _, err := existing.OpenImageExisting(t.Context(), request); err == nil {
		file.Close()
		lease.Close()
		t.Fatal("read-only open accepted wrong client root")
	}
}

func TestHostExistingImageReaderNeverRecreatesMissingLease(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	request, _ := imageDomainPublish(t, store, "c")
	name := filepath.Join(store.root.Name(), request.PlatformDigest[7:]+".lock")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if file, lease, _, err := store.OpenImageExisting(t.Context(), request); err == nil {
		file.Close()
		lease.Close()
		t.Fatal("existing-only reader replaced missing native ownership")
	}
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatal("read-only open recreated a lease generation", err)
	}
}

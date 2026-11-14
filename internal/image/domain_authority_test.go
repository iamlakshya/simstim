package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageHostDomainCannotUseAnotherHomeOrHostIdentity(t *testing.T) {
	for _, other := range []struct{ scope, host string }{{"c", "b"}, {"a", "c"}} {
		t.Run(other.scope+other.host, func(t *testing.T) {
			imageDomainHome(t)
			store := imageDomainStore(t, imageDomainFixture("a", "b"))
			request, _ := imageDomainPublish(t, store, "d")
			before := imageDomainRead(t, store, request.PlatformDigest[7:]+"/image.json")
			// The host adapter supplies the actual placement-derived tuple. This
			// cache test changes HOME as well as its independently supplied tuple.
			otherHome := t.TempDir()
			t.Setenv("HOME", otherHome)
			t.Setenv("USERPROFILE", otherHome)
			foreign, err := OpenHost(t.Context(), store.root.Name(), imageDomainFixture(other.scope, other.host), imageDomainGate)
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			_, err = foreign.Cached(t.Context(), request)
			requireImageDomainError(t, err)
			if lease, err := foreign.Acquire(t.Context(), request.PlatformDigest); err == nil {
				lease.Close()
				t.Fatal("foreign domain acquired a start lease")
			} else {
				requireImageDomainError(t, err)
			}
			if transfer, err := foreign.BeginContext(t.Context(), request); err == nil {
				transfer.Close()
				t.Fatal("foreign domain created a transfer")
			}
			if !bytes.Equal(before, imageDomainRead(t, store, request.PlatformDigest[7:]+"/image.json")) {
				t.Fatal("foreign cache use rewrote image metadata")
			}
		})
	}
}

func TestImageFixtureConstructorsAndEarlierHandlesCannotUseHostObjects(t *testing.T) {
	imageDomainHome(t)
	path := imageDomainPath(t, "images")
	local, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	store, err := OpenHost(t.Context(), path, imageDomainFixture("a", "b"), imageDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request, _ := imageDomainPublish(t, store, "c")
	for _, open := range []func(string) (*Store, error){Open, OpenExisting} {
		opened, err := open(path)
		if err == nil {
			opened.Close()
			t.Fatal("fixture constructor opened host-bound storage")
		}
		requireImageDomainError(t, err)
	}
	_, err = local.Cached(t.Context(), request)
	requireImageDomainError(t, err)
	if lease, err := local.Acquire(t.Context(), request.PlatformDigest); err == nil {
		lease.Close()
		t.Fatal("earlier fixture handle bypassed host lease ownership")
	}
	if _, err := CacheBlob(t.Context(), path, nil, Reference{}, Layer{Digest: request.PlatformDigest, Size: 1}); err == nil {
		t.Fatal("laptop blob constructor fell back into host storage")
	}
}

func TestImageHostNeverAdoptsLegacyLeaseOrMatchingImagePair(t *testing.T) {
	imageDomainHome(t)
	local := imageStore(t)
	request, _ := imageDomainPublish(t, local, "c")
	before := imageDomainRead(t, local, request.PlatformDigest[7:]+"/image.json")
	store, err := OpenHost(t.Context(), local.root.Name(), imageDomainFixture("a", "b"), imageDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Cached(t.Context(), request)
	requireImageDomainError(t, err)
	requireImageDomainError(t, store.Remove(t.Context(), request.PlatformDigest, noImageReferences))
	if _, err := store.Prune(t.Context(), noImageReferences, func() (map[string]bool, error) { return nil, nil }, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("host retention adopted local image records")
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || !containsImageLeft(report, request.PlatformDigest[7:]) || len(report.Removed) != 0 {
		t.Fatalf("legacy image was not retained: %+v, %v", report, err)
	}
	if !bytes.Equal(before, imageDomainRead(t, local, request.PlatformDigest[7:]+"/image.json")) {
		t.Fatal("matching bytes were promoted to host ownership")
	}
}

func TestImageHostMissingOrReplacedLeaseReceiptRefusesBeforeUse(t *testing.T) {
	for _, change := range []string{"missing", "same-bytes-new-inode", "linked"} {
		t.Run(change, func(t *testing.T) {
			imageDomainHome(t)
			store := imageDomainStore(t, imageDomainFixture("a", "b"))
			digest := "sha256:" + strings.Repeat("c", 64)
			lease, err := store.Acquire(t.Context(), digest)
			if err != nil {
				t.Fatal(err)
			}
			lease.Close()
			if change == "missing" {
				if err := store.root.Remove("ownership/" + leaseReceiptName(digest[7:]+".lock")); err != nil {
					t.Fatal(err)
				}
			} else {
				name := filepath.Join(store.root.Name(), digest[7:]+".lock")
				if err := os.Rename(name, name+".foreign"); err != nil {
					t.Fatal(err)
				}
				if change == "linked" {
					if err := os.Link(name+".foreign", name); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(name, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if lease, err := store.Acquire(t.Context(), digest); err == nil {
				lease.Close()
				t.Fatal("missing receipt or native twin acquired host authority")
			}
		})
	}
}

func TestImageHostUnknownDomainMetadataIsPreservedAndNotReplayed(t *testing.T) {
	for _, mutation := range []string{"missing", "foreign", "future", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			imageDomainHome(t)
			store := imageDomainStore(t, imageDomainFixture("a", "b"))
			request, _ := imageDomainPublish(t, store, "c")
			name := imageReceiptName(request.PlatformDigest)
			raw := imageDomainRead(t, store, "ownership/"+name)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "missing":
				delete(fields, "domain")
			case "foreign":
				fields["domain"], _ = json.Marshal(imageDomainFixture("d", "b"))
			case "future":
				fields["domain"] = json.RawMessage(`{"scope":"` + strings.Repeat("a", 64) + `","host_id":"` + strings.Repeat("b", 32) + `","future":true}`)
			}
			raw, _ = json.Marshal(fields)
			if mutation == "duplicate" {
				raw = append([]byte(`{"domain":`+string(fields["domain"])+`,`), raw[1:]...)
			}
			imageDomainReceiptBytes(t, store, name, raw)
			before := imageDomainRead(t, store, "ownership/"+name)
			if _, err := store.Cached(t.Context(), request); err == nil {
				t.Fatal("unknown, missing or foreign domain became read authority")
			}
			report, err := store.PurgeOwned(t.Context(), noImageReferences)
			if err != nil || !containsImageLeft(report, request.PlatformDigest[7:]) {
				t.Fatalf("unproven domain was not inventoried: %+v, %v", report, err)
			}
			if !bytes.Equal(before, imageDomainRead(t, store, "ownership/"+name)) {
				t.Fatal("refusal rewrote unknown receipt bytes")
			}
		})
	}
}

func TestImageHostExistingAndLegacyBeginCannotPublishParts(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	request := Request{Ref: "registry.example/team/base:stable", Digest: "sha256:" + strings.Repeat("c", 64), PlatformDigest: "sha256:" + strings.Repeat("c", 64), Arch: "amd64"}
	if _, err := store.Begin(request); err == nil {
		t.Fatal("host transfer borrowed an ambient background context")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.BeginContext(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transfer lost context identity: %v", err)
	}
	existing, err := OpenHostExisting(t.Context(), store.root.Name(), store.domain)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	if _, err := existing.BeginContext(t.Context(), request); err == nil {
		t.Fatal("existing-only host store created transfer parts")
	}
	if _, err := existing.Cached(t.Context(), request); err == nil {
		t.Fatal("existing-only host store created UsedAt intent")
	}
	entries, err := directoryEntries(store.root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("refused operations created infrastructure beyond original children: %v, %v", entries, err)
	}
}

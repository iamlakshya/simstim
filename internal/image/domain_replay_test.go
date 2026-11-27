package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestImageHostMixedDomainsPurgeOnlyOwnObjectsAndKeepSharedRoots(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	owned, _ := imageDomainPublish(t, store, "c")
	foreign, err := OpenHost(t.Context(), store.root.Name(), imageDomainFixture("d", "e"), imageDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	other, _ := imageDomainPublish(t, foreign, "f")
	otherMetadata := imageDomainRead(t, foreign, other.PlatformDigest[7:]+"/image.json")
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || !containsImageLeft(report, other.PlatformDigest[7:]) {
		t.Fatalf("mixed-domain purge invented total ownership: %+v, %v", report, err)
	}
	if _, err := store.root.Lstat(owned.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned object did not retire: %v", err)
	}
	if !bytes.Equal(otherMetadata, imageDomainRead(t, foreign, other.PlatformDigest[7:]+"/image.json")) {
		t.Fatal("own-domain purge changed foreign metadata")
	}
	for _, name := range []string{"incoming", "ownership", other.PlatformDigest[7:] + ".lock"} {
		if _, err := store.root.Lstat(name); err != nil {
			t.Fatal("mixed-domain purge erased shared or foreign infrastructure")
		}
	}
}

func TestImageHostForeignPreparedPublicationCannotReplayOrRewriteReceipt(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	converted := false
	denied := errors.New("stable publication denied")
	request := Request{Ref: "registry.example/team/base:stable", Digest: "sha256:" + strings.Repeat("c", 64), PlatformDigest: "sha256:" + strings.Repeat("c", 64), Arch: "amd64"}
	store.gate = func(_ context.Context, publish func() error) error {
		if converted {
			if _, err := store.root.Lstat("ownership/" + imageReceiptName(request.PlatformDigest)); err == nil {
				return denied
			}
		}
		return publish()
	}
	input, checked := convertFixture(t)
	_, err := store.publish(t.Context(), request, nil, nil, func(_ context.Context, _ []Layer, _ []*os.File, out *os.File) error {
		data := make([]byte, checked.Size)
		if _, err := input.ReadAt(data, 0); err != nil {
			return err
		}
		_, err := out.Write(data)
		converted = true
		return err
	})
	if !errors.Is(err, denied) {
		t.Fatalf("denied stable publication lost error: %v", err)
	}
	if _, err := store.root.Lstat(request.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied host admission published a stable image")
	}
	receipt, err := store.readOwnership(request.PlatformDigest)
	if err != nil || !stageLeaf(receipt.Leaf) {
		t.Fatalf("denied stable publication has no exact prepared intent: %+v, %v", receipt, err)
	}
	before := imageDomainRead(t, store, "ownership/"+imageReceiptName(request.PlatformDigest))
	foreign, err := OpenHost(t.Context(), store.root.Name(), imageDomainFixture("d", "e"), imageDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	requireImageDomainError(t, foreign.recoverPublication(t.Context(), &receipt))
	if !bytes.Equal(before, imageDomainRead(t, store, "ownership/"+imageReceiptName(request.PlatformDigest))) {
		t.Fatal("foreign prepared replay rewrote the receipt head")
	}
	if _, err := store.root.Lstat(request.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("foreign replay published prepared bytes")
	}
}

func TestImageHostLeaseBackupCannotRecreateAnotherDomainHead(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	name := strings.Repeat("c", 64) + ".lock"
	lease, err := store.lock(t.Context(), name, false)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	root, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	backup := ".receipt-new-" + strings.Repeat("d", 32)
	if err := root.Rename(leaseReceiptName(name), backup); err != nil {
		t.Fatal(err)
	}
	before := imageDomainRead(t, store, "ownership/"+backup)
	foreign, err := OpenHostExisting(t.Context(), store.root.Name(), imageDomainFixture("e", "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	report, err := foreign.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) == 0 || len(report.Removed) != 0 {
		t.Fatalf("foreign backup became cleanup authority: %+v, %v", report, err)
	}
	if !bytes.Equal(before, imageDomainRead(t, store, "ownership/"+backup)) {
		t.Fatal("foreign lease replay changed backup bytes")
	}
	if _, err := root.Lstat(leaseReceiptName(name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("foreign replay reconstructed a trusted receipt head")
	}
}

func TestImageDomainReceiptCodecPreservesFutureFieldsWithoutAuthority(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	request, _ := imageDomainPublish(t, store, "c")
	name := imageReceiptName(request.PlatformDigest)
	data := imageDomainRead(t, store, "ownership/"+name)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_owner"] = json.RawMessage(`{"keep":[1,2]}`)
	data, _ = json.Marshal(fields)
	var receipt imageOwnership
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.known() || receipt.Domain == nil || *receipt.Domain != store.domain {
		t.Fatal("unknown receipt became authoritative or lost typed domain")
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !bytes.Contains(encoded, []byte(`"future_owner":{"keep":[1,2]}`)) {
		t.Fatalf("future receipt fields lost: %s, %v", encoded, err)
	}
	if err := store.writeOwnership(receipt); err == nil {
		t.Fatal("known Domain waived unknown ownership policy")
	}
}

func TestImageHostLeasePurgeIntentBeforeCaptureCanResume(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	name := strings.Repeat("c", 64) + ".lock"
	lease, err := store.lock(t.Context(), name, false)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	receipt, err := store.readLeaseOwnership(leaseReceiptName(name))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Quarantine = ".lease-purge-" + strings.Repeat("d", 32)
	if err := store.writeLeaseOwnership(receipt); err != nil {
		t.Fatal(err)
	}
	if lease, err := store.Acquire(t.Context(), "sha256:"+strings.Repeat("c", 64)); err == nil {
		lease.Close()
		t.Fatal("normal start acquired an active purge lease")
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatalf("exact before-capture purge intent could not resume: %+v, %v", report, err)
	}
	if _, err := store.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resumed exact owned lease remains")
	}
}

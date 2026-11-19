package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This unit fixture runs the actual raw-tar converter on ordinary descriptors
// and pipes. It starts no helper, scope, jail, namespace or VM.
func savedRootDataConverter(ctx context.Context, plan SavedRootPlan, produce func(context.Context, io.Writer) error, output *os.File) (SavedRootReport, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { err := produce(ctx, writer); _ = writer.CloseWithError(err); done <- err }()
	report, err := ConvertSavedRoot(ctx, plan, reader, output)
	_ = reader.CloseWithError(err)
	return report, errors.Join(err, <-done)
}

func prepareSavedData(t *testing.T, store *Store, ref, body string, replace bool) *PreparedSave {
	t.Helper()
	data := flatTar(t, flatFixture{Name: "usr/local/bin/tool", Body: body, Mode: 0755})
	plan := savedTarPlan(t)
	plan.Changes = []string{"/usr/local/bin/tool"}
	pending, err := store.PrepareSave(t.Context(), t.Context(), SaveRequest{Ref: ref, Replace: replace}, plan, func(ctx context.Context, output io.Writer) error {
		_, err := io.Copy(output, &flatContextReader{ctx, bytes.NewReader(data)})
		return err
	}, savedRootDataConverter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pending.Close() })
	return pending
}

func TestSavedIncomingRootRequiresKeepAndSupportsTwoNativeAliases(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	first := prepareSavedData(t, store, "tools", "tool-v1", false)
	if review := first.Review(); review.Validate("tools") != nil || len(review.Flagged) == 0 {
		t.Fatal("the actual converter did not return the required review")
	}
	if records, err := store.List(t.Context()); err != nil || len(records) != 0 {
		t.Fatal("an unreviewed incoming root was published", err)
	}
	kept, err := first.Keep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := kept.Validate("tools", store.domain.HostID); err != nil {
		t.Fatal(err)
	}
	second := prepareSavedData(t, store, "other-tools", "tool-v1", false)
	alias, err := second.Keep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if alias.Image.Digest != kept.Image.Digest {
		t.Fatal("identical root bytes did not retain one digest")
	}
	canonical, err := store.readRecord(kept.Image.Digest[7:])
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Ref != "tools" || canonical.SavedHost.Ref != "tools" || !slices.Contains(canonical.SavedAliases, "other-tools") {
		t.Fatal("another alias rewrote the first producer identity")
	}
	for _, record := range []Record{kept.Image, alias.Image} {
		request := Request{Ref: record.Ref, Digest: record.Digest, PlatformDigest: record.Digest, Arch: record.Arch, RootHash: record.RootHash, DataBlocks: record.DataBlocks, SavedHost: CloneSavedHostV1(record.SavedHost)}
		file, lease, opened, err := store.OpenImageExisting(t.Context(), request)
		if err != nil {
			t.Fatal("the original native descriptor could not be reused by its checked alias", err)
		}
		if err := CheckRecord(request, opened); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		_ = lease.Close()
		request.SavedHost.HostID = "cccccccccccccccccccccccccccccccc"
		if _, _, _, err := store.OpenImageExisting(t.Context(), request); err == nil {
			t.Fatal("saved root bytes were lent to another host identity")
		}
	}
}

func TestSavedRefReplacementRequiresItsExplicitChoice(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	first := prepareSavedData(t, store, "tools", "tool-v1", false)
	old, err := first.Keep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	refused := prepareSavedData(t, store, "tools", "tool-v2", false)
	var detail *proto.Error
	if _, err := refused.Keep(t.Context()); !errors.As(err, &detail) || detail.Code != "image_exists" {
		t.Fatal("an existing host alias was replaced without its explicit choice", err)
	}
	replacement := prepareSavedData(t, store, "tools", "tool-v2", true)
	newer, err := replacement.Keep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if newer.Image.Digest == old.Image.Digest {
		t.Fatal("changed root bytes kept the previous digest")
	}
}

func TestSavedSecretFailureAndActualExpiryKeepNoIncomingRoot(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	plan := savedTarPlan(t)
	plan.Values = map[string]api.Secret{"TOKEN": "current-private-value-0123456789"}
	data := flatTar(t, flatFixture{Name: "etc/tool.conf", Body: string(plan.Values["TOKEN"])})
	pending, err := store.PrepareSave(t.Context(), t.Context(), SaveRequest{Ref: "tools"}, plan, func(_ context.Context, output io.Writer) error { _, err := output.Write(data); return err }, savedRootDataConverter)
	var detail *proto.Error
	if !errors.As(err, &detail) || detail.Code != "image_secret" || pending != nil {
		t.Fatal("a secret-bearing root reached review", err)
	}
	incoming := filepath.Join(store.root.Name(), "incoming")
	if entries, err := os.ReadDir(incoming); err != nil || len(entries) != 0 {
		t.Fatal("secret refusal left an incoming root", err)
	}
	expires := prepareSavedData(t, store, "expires", "ordinary", false)
	// Shorten only this actual timer fixture; production remains one hour.
	expires.timer.Reset(time.Millisecond)
	select {
	case <-expires.Done():
	case <-time.After(time.Second):
		t.Fatal("expiry did not join native incoming cleanup")
	}
	if err := expires.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(incoming); err != nil || len(entries) != 0 {
		t.Fatal("expiry kept an unapproved root", err)
	}
}

func TestSavedDropPreservesAReplacedForeignRoot(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	directory := filepath.Join(store.root.Name(), filepath.FromSlash(pending.stage))
	if err := os.Rename(filepath.Join(directory, "rootfs.img"), filepath.Join(directory, "original-retained")); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign root must remain")
	if err := os.WriteFile(filepath.Join(directory, "rootfs.img"), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pending.Close(); err == nil {
		t.Fatal("drop adopted a replaced path as cleanup authority")
	}
	actual, err := os.ReadFile(filepath.Join(directory, "rootfs.img"))
	if err != nil || !bytes.Equal(actual, foreign) {
		t.Fatal("drop changed a foreign replacement", err)
	}
}

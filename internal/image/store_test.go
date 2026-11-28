package image

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/iamlakshya/simstim/internal/api"
)

func buildTestMultiArchIndex(t *testing.T, extra string) (v1.ImageIndex, v1.Hash) {
	t.Helper()
	makeArch := func(arch string) v1.Image {
		l := makeGzipLayer(t, []tarItem{
			{hdr: tar.Header{Name: "etc", Typeflag: tar.TypeDir, Mode: 0o755}},
			{hdr: tar.Header{Name: "etc/arch", Mode: 0o644}, body: arch + ":" + extra},
			{hdr: tar.Header{Name: "usr/bin/a", Mode: 0o755}, body: "bin"},
			{hdr: tar.Header{Name: "usr/bin/b", Typeflag: tar.TypeLink, Linkname: "usr/bin/a"}},
		}, types.OCILayer)
		img := makeImage(t, l)
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cfg = cfg.DeepCopy()
		cfg.OS = "linux"
		cfg.Architecture = arch
		img, err = mutate.ConfigFile(img, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}

	amd := makeArch("amd64")
	arm := makeArch("arm64")
	amdDigest, err := amd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(
		mutate.IndexMediaType(empty.Index, types.OCIImageIndex),
		mutate.IndexAddendum{
			Add: amd,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: "linux", Architecture: "amd64"},
			},
		},
		mutate.IndexAddendum{
			Add: arm,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: "linux", Architecture: "arm64"},
			},
		},
	)
	return idx, amdDigest
}

func TestStorePullDeterminismRootCheckAndConcurrency(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	idx, amdDigest := buildTestMultiArchIndex(t, "v1")
	idxDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	refStr := host + "/acme/dev:1"
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatalf("write index: %v", err)
	}

	ctx := context.Background()
	popts := PullOptions{Insecure: true}

	resolved, err := Resolve(ctx, refStr, popts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Digest != idxDigest.String() {
		t.Fatalf("resolved digest = %s, want %s", resolved.Digest, idxDigest)
	}
	if resolved.Platforms["amd64"] != amdDigest.String() {
		t.Fatalf("amd64 platform = %s, want %s", resolved.Platforms["amd64"], amdDigest)
	}

	// Pull concurrently on store 1 (two goroutines racing on the same digest).
	s1 := NewStore(filepath.Join(t.TempDir(), "images"))
	var wg sync.WaitGroup
	recs := make([]api.ImageRecord, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			recs[idx], errs[idx] = s1.Pull(ctx, PullRequest{
				Ref:         refStr,
				Digest:      resolved.Digest,
				Arch:        "amd64",
				PullOptions: popts,
			})
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent pull %d failed: %v", i, e)
		}
	}
	if recs[0].RootHash == "" || recs[0].RootHash != recs[1].RootHash {
		t.Fatalf("concurrent pulls disagree: %+v vs %+v", recs[0], recs[1])
	}
	if recs[0].PlatformDigest != amdDigest.String() {
		t.Fatalf("platform digest = %s, want %s", recs[0].PlatformDigest, amdDigest)
	}

	// Second independent store converts the same digest to identical ext4 bytes and root hash.
	s2 := NewStore(filepath.Join(t.TempDir(), "images2"))
	rec2, err := s2.Pull(ctx, PullRequest{
		Ref:              refStr,
		Digest:           resolved.Digest,
		Arch:             "amd64",
		ExpectedRootHash: recs[0].RootHash,
		PullOptions:      popts,
	})
	if err != nil {
		t.Fatalf("second store pull: %v", err)
	}
	if rec2.RootHash != recs[0].RootHash || rec2.DataBlocks != recs[0].DataBlocks {
		t.Fatalf("non-deterministic conversion: %+v vs %+v", rec2, recs[0])
	}
	b1, err := os.ReadFile(s1.ImagePath(amdDigest.Hex))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(s2.ImagePath(amdDigest.Hex))
	if err != nil {
		t.Fatal(err)
	}
	fsBytes := int64(recs[0].DataBlocks) * verityBlockSize
	if !bytes.Equal(b1[:fsBytes], b2[:fsBytes]) {
		t.Fatal("ext4 filesystem bytes differ between two conversions")
	}
	if !bytes.Equal(b1[fsBytes+verityBlockSize:], b2[fsBytes+verityBlockSize:]) {
		t.Fatal("dm-verity Merkle tree bytes differ between two conversions")
	}

	// Wrong expected root hash on fresh conversion fails with image_root_mismatch and leaves no file.
	s3 := NewStore(filepath.Join(t.TempDir(), "images3"))
	_, err = s3.Pull(ctx, PullRequest{
		Ref:              refStr,
		Digest:           resolved.Digest,
		Arch:             "amd64",
		ExpectedRootHash: strings.Repeat("0", 64),
		PullOptions:      popts,
	})
	ie, ok := AsError(err)
	if !ok || ie.Code != CodeRootMismatch || !strings.Contains(ie.Message, "deleted its copy") {
		t.Fatalf("wrong root hash err = %v, want %s saying deleted its copy", err, CodeRootMismatch)
	}
	if _, statErr := os.Stat(s3.ImagePath(amdDigest.Hex)); !os.IsNotExist(statErr) {
		t.Fatalf("failed conversion left file behind: %v", statErr)
	}

	// Wrong expected root hash on an already-stored copy returns image_root_mismatch
	// without falsely claiming the host deleted the existing copy.
	_, err = s2.Pull(ctx, PullRequest{
		Ref:              refStr,
		Digest:           resolved.Digest,
		Arch:             "amd64",
		ExpectedRootHash: strings.Repeat("0", 64),
		PullOptions:      popts,
	})
	ie, ok = AsError(err)
	if !ok || ie.Code != CodeRootMismatch || strings.Contains(ie.Message, "deleted its copy") {
		t.Fatalf("cached root mismatch err = %v, want %s without deleted-copy claim", err, CodeRootMismatch)
	}
	if _, statErr := os.Stat(s2.ImagePath(amdDigest.Hex)); statErr != nil {
		t.Fatalf("cached image unexpectedly disappeared: %v", statErr)
	}

	// Prefix lookup and in-use protection on Remove.
	got, imgPath, err := s1.Get(amdDigest.Hex[:12])
	if err != nil || got.PlatformDigest != amdDigest.String() || imgPath == "" {
		t.Fatalf("Get prefix: rec=%+v path=%q err=%v", got, imgPath, err)
	}
	_, err = s1.Remove(amdDigest.Hex[:12], func(api.ImageRecord) string { return "auth-fix" })
	ie, ok = AsError(err)
	if !ok || ie.Code != CodeInUse {
		t.Fatalf("Remove in-use err = %v, want %s", err, CodeInUse)
	}
	if _, err := s1.Remove(amdDigest.Hex[:12], nil); err != nil {
		t.Fatalf("Remove unused: %v", err)
	}
	list, err := s1.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("List after Remove = %v, err=%v", list, err)
	}
}

func TestStorePruneRules(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s := NewStore(filepath.Join(t.TempDir(), "images"))
	s.Now = func() time.Time { return now }

	installFake := func(tag string, convertedAt, usedAt, fileMod time.Time) api.ImageRecord {
		img := makeImage(t, makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: "f"}, body: tag},
		}))
		d, _ := img.Digest()
		rec, err := s.InstallImage("example.com/"+tag+":1", d.String(), d.String(), "amd64", "", img, Limits{})
		if err != nil {
			t.Fatalf("install %s: %v", tag, err)
		}
		rec.ConvertedAt = convertedAt
		rec.UsedAt = usedAt
		if err := s.writeRecord(d.Hex, rec); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(s.ImagePath(d.Hex), fileMod, fileMod)
		return rec
	}

	old := now.Add(-10 * 24 * time.Hour)
	recSession := installFake("session", old, old, old)
	recDefault := installFake("default", old, old, old)
	recRecentUse := installFake("recent-use", old, now.Add(-2*24*time.Hour), old)
	recArrived := installFake("arrived", old, old, now.Add(-10*time.Minute))
	recExpired := installFake("expired", old, old, old)

	removed, err := s.Prune(PruneKeep{
		SessionImages: []string{recSession.PlatformDigest},
		DefaultImages: []string{recDefault.PlatformDigest},
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 1 || removed[0] != recExpired.PlatformDigest {
		t.Fatalf("removed = %v, want [%s]", removed, recExpired.PlatformDigest)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("remaining = %d, want 4 (%v)", len(list), list)
	}
	_ = recRecentUse
	_ = recArrived
}

func TestFetchFromLayoutAndManifestCap(t *testing.T) {
	idx, amdDigest := buildTestMultiArchIndex(t, "layout")
	idxDigest, _ := idx.Digest()
	dir := filepath.Join(t.TempDir(), "oci")
	if _, err := layout.Write(dir, idx); err != nil {
		t.Fatalf("layout.Write: %v", err)
	}
	img, platDigest, err := FetchFromLayout(dir, idxDigest.String(), "amd64")
	if err != nil {
		t.Fatalf("FetchFromLayout: %v", err)
	}
	if platDigest != amdDigest.String() || img == nil {
		t.Fatalf("got plat=%s, want %s", platDigest, amdDigest)
	}

	// Single-manifest layout enforces requested platform architecture.
	armImg := makeImage(t, makeUncompressedLayer(t, []tarItem{{hdr: tar.Header{Name: "f"}, body: "arm"}}))
	cfg, err := armImg.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS = "linux"
	cfg.Architecture = "arm64"
	armImg, err = mutate.ConfigFile(armImg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	armDig, err := armImg.Digest()
	if err != nil {
		t.Fatal(err)
	}
	singleDir := filepath.Join(t.TempDir(), "oci-arm64")
	lp, err := layout.Write(singleDir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := lp.AppendImage(armImg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FetchFromLayout(singleDir, armDig.String(), "amd64"); err == nil {
		t.Fatal("FetchFromLayout accepted arm64 single-manifest layout for amd64")
	} else if ie, ok := AsError(err); !ok || ie.Code != CodeNotFound {
		t.Fatalf("wrong arch layout err = %v, want %s", err, CodeNotFound)
	}
	if gotImg, gotDig, err := FetchFromLayout(singleDir, armDig.String(), "arm64"); err != nil || gotImg == nil || gotDig != armDig.String() {
		t.Fatalf("FetchFromLayout arm64 = (%v, %s, %v)", gotImg != nil, gotDig, err)
	}

	// checkPlatformArch fails closed when ConfigFile returns an error.
	if err := checkPlatformArch(brokenConfigImage{Image: armImg}, "test:broken", "arm64"); err == nil {
		t.Fatal("checkPlatformArch ignored ConfigFile error")
	}

	// Oversized manifest rejected by transport cap.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", string(types.OCIManifestSchema1))
		_, _ = w.Write(bytes.Repeat([]byte(" "), 2048))
	}))
	defer big.Close()
	host := strings.TrimPrefix(big.URL, "http://")
	_, err = Resolve(context.Background(), host+"/acme/big:1", PullOptions{
		Insecure:         true,
		MaxManifestBytes: 512,
	})
	ie, ok := AsError(err)
	if !ok || ie.Code != CodeBadLayer {
		t.Fatalf("oversized manifest err = %v, want %s", err, CodeBadLayer)
	}
}

func TestLockFilePinUpdateAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.lock")
	d1 := "sha256:" + strings.Repeat("a", 64)
	d2 := "sha256:" + strings.Repeat("b", 64)

	entry, created, prev, err := Pin(path, "ghcr.io/acme/dev:1", d1, "box", false)
	if err != nil || !created || prev != "" || entry.Digest != d1 {
		t.Fatalf("first Pin: entry=%+v created=%v prev=%q err=%v", entry, created, prev, err)
	}

	// Moved tag fails with image_moved.
	_, _, _, err = Pin(path, "ghcr.io/acme/dev:1", d2, "box", false)
	ie, ok := AsError(err)
	if !ok || ie.Code != CodeMoved {
		t.Fatalf("moved Pin err = %v, want %s", err, CodeMoved)
	}

	// Record first conversion root hash, then mismatch fails.
	if err := RecordPlatform(path, "ghcr.io/acme/dev:1", d1, LockPlatform{
		Arch: "amd64", Digest: d1, RootHash: strings.Repeat("1", 64), DataBlocks: 10, RootFrom: "box",
	}); err != nil {
		t.Fatalf("RecordPlatform first: %v", err)
	}
	err = RecordPlatform(path, "ghcr.io/acme/dev:1", d1, LockPlatform{
		Arch: "amd64", Digest: d1, RootHash: strings.Repeat("2", 64), DataBlocks: 10, RootFrom: "nuc",
	})
	ie, ok = AsError(err)
	if !ok || ie.Code != CodeRootMismatch || strings.Contains(ie.Message, "deleted its copy") {
		t.Fatalf("mismatch RecordPlatform err = %v, want %s without deleted-copy claim", err, CodeRootMismatch)
	}

	// --update moves the pin and clears old platform hashes.
	entry, created, prev, err = Pin(path, "ghcr.io/acme/dev:1", d2, "box", true)
	if err != nil || created || prev != d1 || entry.Digest != d2 || len(entry.Platforms) != 0 {
		t.Fatalf("update Pin: entry=%+v created=%v prev=%q err=%v", entry, created, prev, err)
	}

	// Concurrent pins for distinct refs both stay in images.lock.
	var wg sync.WaitGroup
	for i, ch := range []string{"c", "d", "e"} {
		wg.Add(1)
		go func(i int, ch string) {
			defer wg.Done()
			ref := "ghcr.io/acme/r" + string(rune('0'+i)) + ":1"
			dig := "sha256:" + strings.Repeat(ch, 64)
			if _, _, _, err := Pin(path, ref, dig, "box", false); err != nil {
				t.Errorf("concurrent Pin %s: %v", ref, err)
			}
		}(i, ch)
	}
	wg.Wait()
	lf, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lf.Images) != 4 {
		t.Fatalf("lock entries = %d, want 4", len(lf.Images))
	}
}

type brokenConfigImage struct{ v1.Image }

func (brokenConfigImage) ConfigFile() (*v1.ConfigFile, error) {
	return nil, errors.New("corrupt config blob")
}

func TestStorePullFastPathAndRemoveRace(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	idx, amdDigest := buildTestMultiArchIndex(t, "race")
	idxDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	refStr := host + "/acme/race:1"
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}

	s := NewStore(filepath.Join(t.TempDir(), "images"))
	req := PullRequest{
		Ref:         refStr,
		Digest:      idxDigest.String(),
		Arch:        "amd64",
		PullOptions: PullOptions{Insecure: true},
	}
	if _, err := s.Pull(context.Background(), req); err != nil {
		t.Fatalf("seed Pull: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 8 {
			if _, err := s.Pull(context.Background(), req); err != nil {
				t.Errorf("concurrent Pull: %v", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 8 {
			_, _ = s.Remove(amdDigest.Hex, nil)
		}
	}()
	wg.Wait()
}

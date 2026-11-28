package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func sha256HexBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func useTempHostDirs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
}

func allowTestRuntimeHTTP(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldAllow := allowInsecureRuntimeURL
	oldClient := runtimeHTTPClient
	allowInsecureRuntimeURL = true
	if srv != nil {
		runtimeHTTPClient = srv.Client
	}
	t.Cleanup(func() {
		allowInsecureRuntimeURL = oldAllow
		runtimeHTTPClient = oldClient
	})
}

func TestFetchRuntimeModesVerificationAndIdempotency(t *testing.T) {
	useTempHostDirs(t)

	kernelBytes := bytes.Repeat([]byte("K"), 4096)
	vmmBytes := bytes.Repeat([]byte("V"), 2048)
	gvisorBytes := bytes.Repeat([]byte("G"), 1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kernel":
			_, _ = w.Write(kernelBytes)
		case "/vmm":
			_, _ = w.Write(vmmBytes)
		case "/runsc":
			_, _ = w.Write(gvisorBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	allowTestRuntimeHTTP(t, srv)

	items := []api.RuntimeAsset{
		{Kind: "kernel", Name: "guest", Arch: "amd64", SHA256: sha256HexBytes(kernelBytes), Size: int64(len(kernelBytes)), URL: srv.URL + "/kernel"},
		{Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64", SHA256: strings.ToUpper(sha256HexBytes(vmmBytes)), Size: int64(len(vmmBytes)), URL: srv.URL + "/vmm"},
		{Kind: "gvisor", Name: "runsc", Arch: "amd64", SHA256: "sha256:" + sha256HexBytes(gvisorBytes), Size: int64(len(gvisorBytes)), URL: srv.URL + "/runsc"},
	}

	res, err := FetchRuntime(context.Background(), api.RuntimeFetchArgs{
		Release: "0.9.0",
		Runtime: "2026.11.0",
		Items:   items,
	})
	if err != nil {
		t.Fatalf("FetchRuntime: %v", err)
	}
	if len(res.Installed) != 3 {
		t.Fatalf("installed = %d, want 3", len(res.Installed))
	}

	wantModes := map[string]os.FileMode{
		sha256HexBytes(kernelBytes): 0o444,
		sha256HexBytes(vmmBytes):    0o555,
		sha256HexBytes(gvisorBytes): 0o555,
	}
	for digest, wantMode := range wantModes {
		fi, err := os.Stat(paths.ArtefactFile(digest))
		if err != nil {
			t.Fatalf("missing artefact %s: %v", digest, err)
		}
		if fi.Mode().Perm() != wantMode {
			t.Errorf("artefact %s mode = %04o, want %04o", digest[:12], fi.Mode().Perm(), wantMode)
		}
	}

	if _, err := os.Stat(paths.RuntimeLabelFile("2026.11.0")); err != nil {
		t.Fatalf("label manifest missing: %v", err)
	}
	if entries, err := os.ReadDir(paths.ArtefactsIncomingDir()); err != nil || len(entries) != 0 {
		t.Errorf("incoming dir not empty: %+v, err=%v", entries, err)
	}

	// Second call skips downloading and marks AlreadyPresent.
	res2, err := FetchRuntime(context.Background(), api.RuntimeFetchArgs{
		Release: "0.9.0",
		Runtime: "2026.11.0",
		Items:   items,
	})
	if err != nil {
		t.Fatalf("second FetchRuntime: %v", err)
	}
	for _, f := range res2.Installed {
		if !f.AlreadyPresent {
			t.Errorf("expected AlreadyPresent for %s: %+v", f.Name, f)
		}
	}

	st := RuntimeStatusReport()
	if len(st.Runtimes) != 1 || st.Runtimes[0] != "2026.11.0" || len(st.Files) != 3 {
		t.Fatalf("RuntimeStatusReport = %+v", st)
	}
}

func TestFetchRuntimeRejectsBadDigestOversizeTruncatedAndHTTP(t *testing.T) {
	useTempHostDirs(t)

	good := bytes.Repeat([]byte("A"), 512)
	wrong := bytes.Repeat([]byte("B"), 512)
	oversize := bytes.Repeat([]byte("C"), 600)
	short := bytes.Repeat([]byte("D"), 100)

	// Non-HTTPS URL must be refused when allowInsecureRuntimeURL is false.
	_, err := FetchRuntime(context.Background(), api.RuntimeFetchArgs{
		Items: []api.RuntimeAsset{{
			Kind: "kernel", Name: "guest", SHA256: sha256HexBytes(good), Size: int64(len(good)), URL: "http://example.com/kernel",
		}},
	})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "bad_request" {
		t.Fatalf("HTTP URL err = %v, want bad_request", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wrong":
			_, _ = w.Write(wrong)
		case "/oversize-cl":
			_, _ = w.Write(oversize)
		case "/oversize-stream":
			// Flush first so Go uses chunked transfer with ContentLength == -1.
			fl, _ := w.(http.Flusher)
			_, _ = w.Write(oversize[:256])
			if fl != nil {
				fl.Flush()
			}
			_, _ = w.Write(oversize[256:])
		case "/short":
			_, _ = w.Write(short)
		}
	}))
	defer srv.Close()
	allowTestRuntimeHTTP(t, srv)

	wantHex := sha256HexBytes(good)
	for _, tc := range []struct {
		name string
		path string
		code string
	}{
		{"wrong sha256", "/wrong", "digest_mismatch"},
		{"oversize content-length", "/oversize-cl", "runtime_too_big"},
		{"oversize stream", "/oversize-stream", "runtime_too_big"},
		{"truncated", "/short", "digest_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FetchRuntime(context.Background(), api.RuntimeFetchArgs{
				Items: []api.RuntimeAsset{{
					Kind: "kernel", Name: "guest", SHA256: wantHex, Size: int64(len(good)), URL: srv.URL + tc.path,
				}},
			})
			var pe *proto.Error
			if !errors.As(err, &pe) || pe.Code != tc.code {
				t.Fatalf("%s err = %v, want code %s", tc.name, err, tc.code)
			}
			if _, err := os.Stat(paths.ArtefactFile(wantHex)); !os.IsNotExist(err) {
				t.Fatalf("bad artefact was stored: %v", err)
			}
			if entries, err := os.ReadDir(paths.ArtefactsIncomingDir()); err == nil && len(entries) != 0 {
				t.Fatalf("part file was not deleted after %s: %+v", tc.name, entries)
			}
		})
	}
}

func TestFetchRuntimeConcurrentTransfersAndActiveProgress(t *testing.T) {
	useTempHostDirs(t)

	payload := bytes.Repeat([]byte("R"), 64*1024)
	wantHex := sha256HexBytes(payload)

	release := make(chan struct{})
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl, _ := w.(http.Flusher)
		_, _ = w.Write(payload[:len(payload)/2])
		if fl != nil {
			fl.Flush()
		}
		started <- struct{}{}
		<-release
		_, _ = w.Write(payload[len(payload)/2:])
	}))
	defer srv.Close()
	allowTestRuntimeHTTP(t, srv)

	args := api.RuntimeFetchArgs{
		Runtime: "2026.11.0",
		Items: []api.RuntimeAsset{{
			Kind: "vmm", Name: "cloud-hypervisor", SHA256: wantHex, Size: int64(len(payload)), URL: srv.URL + "/vmm",
		}},
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = FetchRuntime(context.Background(), args)
		}(i)
	}

	// Wait until both concurrent downloads are mid-stream.
	for range 2 {
		<-started
	}

	// Verify incoming holds two distinct .part files and RuntimeStatusReport sees active transfers.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := RuntimeStatusReport()
		if len(st.Active) == 2 && st.Active[0].Bytes > 0 && st.Active[1].Bytes > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := RuntimeStatusReport()
	if len(st.Active) != 2 {
		close(release)
		wg.Wait()
		t.Fatalf("active transfers = %+v, want 2", st.Active)
	}
	entries, err := os.ReadDir(paths.ArtefactsIncomingDir())
	if err != nil || len(entries) != 2 {
		close(release)
		wg.Wait()
		t.Fatalf("incoming part files during race = %d, want 2 (err=%v)", len(entries), err)
	}

	close(release)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent fetch %d failed: %v", i, e)
		}
	}
	got, err := os.ReadFile(paths.ArtefactFile(wantHex))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored file mismatch: len=%d err=%v", len(got), err)
	}
	if entries, err := os.ReadDir(paths.ArtefactsIncomingDir()); err != nil || len(entries) != 0 {
		t.Fatalf("leftover incoming parts after race: %+v, err=%v", entries, err)
	}
}

func TestInstallRuntimePartChecksAndCleanup(t *testing.T) {
	useTempHostDirs(t)
	if err := ensureArtefactDirs(); err != nil {
		t.Fatal(err)
	}

	data := []byte("verified-tools-image-bytes")
	wantHex := sha256HexBytes(data)
	goodPart := wantHex + ".1111.part"
	if err := os.WriteFile(filepath.Join(paths.ArtefactsIncomingDir(), goodPart), data, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := InstallRuntime(api.RuntimeInstallArgs{
		Release: "0.9.0",
		Runtime: "2026.11.0",
		Items: []api.RuntimeInstallEntry{{
			Asset: api.RuntimeAsset{Kind: "tools", Name: "tools", SHA256: wantHex, Size: int64(len(data))},
			Part:  goodPart,
		}},
	})
	if err != nil {
		t.Fatalf("InstallRuntime good: %v", err)
	}
	if len(res.Installed) != 1 || res.Installed[0].SHA256 != wantHex {
		t.Fatalf("InstallRuntime result = %+v", res)
	}
	fi, err := os.Stat(paths.ArtefactFile(wantHex))
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("installed file stat = %v, err=%v", fi, err)
	}

	// Wrong digest deletes the uploaded part file.
	badPart := wantHex + ".bad.part"
	if err := os.WriteFile(filepath.Join(paths.ArtefactsIncomingDir(), badPart), []byte("corrupted-tools-image-byte"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InstallRuntime(api.RuntimeInstallArgs{
		Items: []api.RuntimeInstallEntry{{
			Asset: api.RuntimeAsset{Kind: "tools", Name: "tools", SHA256: wantHex, Size: int64(len(data))},
			Part:  badPart,
		}},
	})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "digest_mismatch" {
		t.Fatalf("bad part err = %v, want digest_mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ArtefactsIncomingDir(), badPart)); !os.IsNotExist(err) {
		t.Fatalf("bad part was not deleted: %v", err)
	}

	// Oversize part stops and deletes the part file.
	bigPart := wantHex + ".big.part"
	if err := os.WriteFile(filepath.Join(paths.ArtefactsIncomingDir(), bigPart), append(append([]byte(nil), data...), 'X'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InstallRuntime(api.RuntimeInstallArgs{
		Items: []api.RuntimeInstallEntry{{
			Asset: api.RuntimeAsset{Kind: "tools", Name: "tools", SHA256: wantHex, Size: int64(len(data))},
			Part:  bigPart,
		}},
	})
	if !errors.As(err, &pe) || pe.Code != "runtime_too_big" {
		t.Fatalf("oversize part err = %v, want runtime_too_big", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ArtefactsIncomingDir(), bigPart)); !os.IsNotExist(err) {
		t.Fatalf("oversize part was not deleted: %v", err)
	}

	// Symlink and path traversal are refused.
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	symPart := wantHex + ".sym.part"
	if err := os.Symlink(target, filepath.Join(paths.ArtefactsIncomingDir(), symPart)); err != nil {
		t.Fatal(err)
	}
	_, err = InstallRuntime(api.RuntimeInstallArgs{
		Items: []api.RuntimeInstallEntry{{
			Asset: api.RuntimeAsset{Kind: "tools", Name: "tools", SHA256: wantHex, Size: int64(len(data))},
			Part:  symPart,
		}},
	})
	if !errors.As(err, &pe) || pe.Code != "bad_request" {
		t.Fatalf("symlink part err = %v, want bad_request", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
	_, err = InstallRuntime(api.RuntimeInstallArgs{
		Items: []api.RuntimeInstallEntry{{
			Asset: api.RuntimeAsset{Kind: "tools", Name: "tools", SHA256: wantHex, Size: int64(len(data))},
			Part:  "../escape.part",
		}},
	})
	if !errors.As(err, &pe) || pe.Code != "bad_request" {
		t.Fatalf("traversal part err = %v, want bad_request", err)
	}
}

func TestPruneRuntimeAndOldRuntimeFix(t *testing.T) {
	useTempHostDirs(t)
	if err := ensureArtefactDirs(); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-3 * pruneGrace)
	putArtefact := func(content string, at time.Time) string {
		b := []byte(content)
		h := sha256HexBytes(b)
		p := paths.ArtefactFile(h)
		if err := os.WriteFile(p, b, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return h
	}

	hNew1 := putArtefact("newest-1", oldTime)
	hNew2 := putArtefact("newest-2", oldTime)
	hSessionLabel := putArtefact("session-label", oldTime)
	hSessionFile := putArtefact("session-file", oldTime)
	hFresh := putArtefact("arrived-now", time.Now())
	hOldUnused := putArtefact("old-unused", oldTime)

	// Write 4 runtime labels with distinct timestamps (all older than 1h).
	writeLabelAt := func(label string, at time.Time, digests ...string) {
		t.Helper()
		var items []api.RuntimeAsset
		for _, d := range digests {
			items = append(items, api.RuntimeAsset{Kind: "kernel", Name: "guest", SHA256: d, Size: 8})
		}
		if err := writeStoredRuntimeLabel(label, "0.9.0", items); err != nil {
			t.Fatal(err)
		}
		// Patch RecordedAt so readStoredRuntimeLabels orders deterministically.
		p := paths.RuntimeLabelFile(label)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// Replace the auto-generated UTC timestamp with `at`.
		nowLine := time.Now().UTC().Format("2006-")
		_ = nowLine
		idx := bytes.Index(b, []byte(`"recorded_at": "`))
		end := bytes.IndexByte(b[idx+len(`"recorded_at": "`):], '"')
		patched := append([]byte(nil), b[:idx+len(`"recorded_at": "`)]...)
		patched = append(patched, []byte(at.UTC().Format(time.RFC3339Nano))...)
		patched = append(patched, b[idx+len(`"recorded_at": "`)+end:]...)
		if err := os.WriteFile(p, patched, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	writeLabelAt("2026.08.0", oldTime.Add(-3*time.Hour), hOldUnused)
	writeLabelAt("2026.09.0", oldTime.Add(-2*time.Hour), hSessionLabel)
	writeLabelAt("2026.10.0", oldTime.Add(-1*time.Hour), hNew2)
	writeLabelAt("2026.11.0", oldTime, hNew1)

	// Non-deleted session manifest referencing label 2026.09.0 and direct file hSessionFile.
	m := &Manifest{
		Schema: 1,
		ID:     "sess1234",
		Name:   "keep-me",
		Wall: &WallRecord{
			Level:        api.LevelMicroVM,
			Driver:       "chv",
			Runtime:      "2026.09.0",
			RuntimeFiles: []string{hSessionFile},
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	// Stale and fresh incoming .part files.
	stalePart := filepath.Join(paths.ArtefactsIncomingDir(), "stale.part")
	freshPart := filepath.Join(paths.ArtefactsIncomingDir(), "fresh.part")
	if err := os.WriteFile(stalePart, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stalePart, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshPart, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Verify old-runtime doctor fix finds the exact old items before applying.
	fixes := Fixes(api.FixArgs{})
	var rtFix *api.Fix
	for i := range fixes {
		if fixes[i].ID == "old-runtime" {
			rtFix = &fixes[i]
		}
	}
	if rtFix == nil {
		t.Fatalf("expected old-runtime fix in %+v", fixes)
	}
	for _, want := range []string{hOldUnused, "2026.08.0.json", "stale.part"} {
		if !strings.Contains(rtFix.Command, want) {
			t.Errorf("old-runtime fix command missing %q: %s", want, rtFix.Command)
		}
	}

	// Apply via Fixes(old-runtime).
	applied := Fixes(api.FixArgs{Apply: true, IDs: []string{"old-runtime"}})
	done := false
	for _, f := range applied {
		if f.ID == "old-runtime" && f.Done && f.Error == "" {
			done = true
		}
	}
	if !done {
		t.Fatalf("old-runtime fix did not succeed: %+v", applied)
	}

	// Verify kept files still exist and old ones were removed.
	for _, kept := range []string{
		paths.ArtefactFile(hNew1),
		paths.ArtefactFile(hNew2),
		paths.ArtefactFile(hSessionLabel),
		paths.ArtefactFile(hSessionFile),
		paths.ArtefactFile(hFresh),
		paths.RuntimeLabelFile("2026.11.0"),
		paths.RuntimeLabelFile("2026.10.0"),
		paths.RuntimeLabelFile("2026.09.0"),
		freshPart,
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("expected %s to be kept: %v", kept, err)
		}
	}
	for _, gone := range []string{
		paths.ArtefactFile(hOldUnused),
		paths.RuntimeLabelFile("2026.08.0"),
		stalePart,
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned, stat err = %v", gone, err)
		}
	}

	// PruneRuntime on an already-clean cache removes nothing.
	removed, err := PruneRuntime()
	if err != nil || len(removed) != 0 {
		t.Fatalf("second PruneRuntime = (%v, %v), want empty", removed, err)
	}
}

package helpers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// releaseKey matches the key in an allowed-signers line: its type and its
// base64 body.
var releaseKey = regexp.MustCompile(`(?:sk-)?(?:ssh-ed25519|ecdsa-sha2-nistp[0-9]+|ssh-rsa)(?:@openssh\.com)? [A-Za-z0-9+/]+=*`)

// signerKeys returns the keys of the allowed-signers lines in text, skipping
// comments, and checks that each line trusts its key for release files only.
func signerKeys(t *testing.T, where, text string) []string {
	t.Helper()
	var keys []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, `release namespaces="file" `) {
			t.Errorf("%s: %q is not a line for the principal release and the namespace file", where, line)
		}
		k := releaseKey.FindString(line)
		if k == "" {
			t.Errorf("%s: %q holds no key", where, line)
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The keys in the code are the ones in .github/release-signers and in
// install.sh: the same list, in all three, also while a rotation lists two.
func TestReleaseSignersMatchTheRepo(t *testing.T) {
	code := signerKeys(t, "ReleaseSigners", ReleaseSigners)
	if len(code) == 0 {
		t.Fatal("ReleaseSigners holds no key")
	}
	b, err := os.ReadFile("../../../.github/release-signers")
	if err != nil {
		t.Fatal(err)
	}
	if got := signerKeys(t, ".github/release-signers", string(b)); !slices.Equal(got, code) {
		t.Errorf(".github/release-signers lists %q, the code %q", got, code)
	}
	b, err = os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(b), "\nRELEASE_SIGNERS='")
	block, _, closed := strings.Cut(block, "'")
	if !ok || !closed {
		t.Fatal("install.sh has no RELEASE_SIGNERS='...' block")
	}
	if got := signerKeys(t, "install.sh", block); !slices.Equal(got, code) {
		t.Errorf("install.sh lists %q, the code %q", got, code)
	}
}

// newSigner makes a throwaway key in dir and returns its path and its
// allowed-signers line for release files.
func newSigner(t *testing.T, dir, name string) (key, line string) {
	t.Helper()
	key = filepath.Join(dir, name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(pub))
	return key, `release namespaces="file" ` + f[0] + " " + f[1]
}

// fakeRelease serves a signed release with a helper bundle, signed by a
// throwaway key that the test trusts instead of the release key.
func fakeRelease(t *testing.T, version string, tamper bool) *httptest.Server {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("needs ssh-keygen")
	}
	key, line := newSigner(t, t.TempDir(), "key")
	old := trusted
	trusted = line
	t.Cleanup(func() { trusted = old })
	return serveRelease(t, version, tamper, key)
}

// serveRelease serves a release with a helper bundle, whose checksums.txt
// the key signs.
func serveRelease(t *testing.T, version string, tamper bool, key string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	var bundle bytes.Buffer
	zw := gzip.NewWriter(&bundle)
	tw := tar.NewWriter(zw)
	for name, body := range map[string]string{"host-linux-amd64": "HOST", "vt-linux-amd64": "VT"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = zw.Close()
	sum := sha256.Sum256(bundle.Bytes())
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), BundleName(version))
	sumsFile := filepath.Join(dir, "checksums.txt")
	_ = os.WriteFile(sumsFile, []byte(sums), 0o600)
	if out, err := exec.Command("ssh-keygen", "-q", "-Y", "sign", "-n", "file", "-f", key, sumsFile).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	sig, _ := os.ReadFile(sumsFile + ".sig")
	if tamper {
		sums = strings.Replace(sums, "  ", "  x", 1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "checksums.txt":
			_, _ = w.Write([]byte(sums))
		case "checksums.txt.sig":
			_, _ = w.Write(sig)
		case BundleName(version):
			_, _ = w.Write(bundle.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resetFetched makes this test a build without embedded helpers, with none
// fetched yet.
func resetFetched(t *testing.T) {
	t.Helper()
	saved := files
	files = embed.FS{}
	t.Cleanup(func() { files = saved })
	fetchMu.Lock()
	fetched = nil
	fetchMu.Unlock()
	t.Cleanup(func() {
		fetchMu.Lock()
		fetched = nil
		fetchMu.Unlock()
	})
}

func TestFetchVerifiesAndCaches(t *testing.T) {
	resetFetched(t)
	srv := fakeRelease(t, "9.9.9", false)
	cache := t.TempDir()
	if err := Fetch(context.Background(), "9.9.9", srv.URL, cache); err != nil {
		t.Fatal(err)
	}
	if b, ok := For("linux", "amd64"); !ok || string(b) != "HOST" {
		t.Fatalf("helper = %q, %v", b, ok)
	}
	if b, ok := VT("linux", "amd64"); !ok || string(b) != "VT" {
		t.Fatalf("vt = %q, %v", b, ok)
	}
	if Version() == "" || len(Platforms()) != 1 {
		t.Errorf("version %q, platforms %v", Version(), Platforms())
	}
	// A second client reads the cache, with no network.
	resetFetched(t)
	srv.Close()
	if err := Fetch(context.Background(), "9.9.9", srv.URL, cache); err != nil {
		t.Fatalf("from the cache: %v", err)
	}
}

func TestFetchRefusesAChangedRelease(t *testing.T) {
	resetFetched(t)
	srv := fakeRelease(t, "9.9.9", true)
	err := Fetch(context.Background(), "9.9.9", srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err = %v, want a signature error", err)
	}
	if _, ok := For("linux", "amd64"); ok {
		t.Error("a helper from a bad release was kept")
	}
}

// While a rotation lists two keys, a release signed by either key installs
// its helpers, and one signed by a third key does not.
func TestFetchAcceptsAnyListedKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("needs ssh-keygen")
	}
	dir := t.TempDir()
	oldKey, oldLine := newSigner(t, dir, "old")
	newKey, newLine := newSigner(t, dir, "new")
	otherKey, _ := newSigner(t, dir, "other")
	saved := trusted
	trusted = oldLine + "\n" + newLine
	t.Cleanup(func() { trusted = saved })
	for _, c := range []struct {
		key  string
		pass bool
	}{{oldKey, true}, {newKey, true}, {otherKey, false}} {
		resetFetched(t)
		srv := serveRelease(t, "9.9.9", false, c.key)
		err := Fetch(context.Background(), "9.9.9", srv.URL, t.TempDir())
		if c.pass && err != nil {
			t.Errorf("signed by %s: %v", filepath.Base(c.key), err)
		}
		if !c.pass && (err == nil || !strings.Contains(err.Error(), "signature")) {
			t.Errorf("signed by %s, which is not listed: err = %v, want a signature error", filepath.Base(c.key), err)
		}
	}
}

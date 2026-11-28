package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
)

func pushTestImage(t *testing.T, srvURL, repoTag, content string) string {
	t.Helper()
	var tbuf bytes.Buffer
	tw := tar.NewWriter(&tbuf)
	_ = tw.WriteHeader(&tar.Header{Name: "etc", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "etc/hello", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
	_, _ = io.WriteString(tw, content)
	_ = tw.Close()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(tbuf.Bytes())
		_ = zw.Close()
		return io.NopCloser(bytes.NewReader(gz.Bytes())), nil
	}, tarball.WithMediaType(types.OCILayer), tarball.WithCompressedCaching)
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := img.ConfigFile()
	cfg = cfg.DeepCopy()
	cfg.OS = "linux"
	cfg.Architecture = "amd64"
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	refStr := strings.TrimPrefix(srvURL, "http://") + "/" + repoTag
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	d, _ := img.Digest()
	return d.String()
}

func TestImageCommandsPullListAndRemove(t *testing.T) {
	newTestHost(t)
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	// Point host/client at the local HTTP test registry (127.0.0.1 is allowed by
	// go-containerregistry's name.ParseReference automatically for localhost/127.0.0.1).
	refStr := strings.TrimPrefix(srv.URL, "http://") + "/acme/dev:1"
	d1 := pushTestImage(t, srv.URL, "acme/dev:1", "first")

	// Empty list on host.
	out := okJSON(t, "image", "ls", "box", "--json")
	var emptyList []api.ImageRecord
	if err := json.Unmarshal([]byte(out), &emptyList); err != nil || len(emptyList) != 0 {
		t.Fatalf("empty image ls = %q, err=%v", out, err)
	}

	// Pull pins the tag in images.lock and stores the converted image on box.
	pullJSON := okJSON(t, "image", "pull", refStr, "box", "--json")
	var pulled api.ImageRecord
	if err := json.Unmarshal([]byte(pullJSON), &pulled); err != nil {
		t.Fatalf("decode image pull json: %v\n%s", err, pullJSON)
	}
	if pulled.PlatformDigest != d1 || pulled.RootHash == "" {
		t.Fatalf("unexpected pull record: %+v", pulled)
	}

	// images.lock recorded the pin and root hash.
	lf, err := image.LoadLock(client.ImagesLockPath())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lf.Find(refStr)
	if !ok || entry.Digest != d1 {
		t.Fatalf("images.lock missing pin: %+v", lf)
	}
	if p, ok := entry.Platform(pulled.Arch); !ok || p.RootHash != pulled.RootHash {
		t.Fatalf("images.lock missing platform root hash: %+v", entry)
	}

	// Tag moves: pull without --update fails with image_moved (KindRefused).
	d2 := pushTestImage(t, srv.URL, "acme/dev:1", "second")
	_, _, err = runApart(t, "image", "pull", refStr, "box")
	if err == nil {
		t.Fatal("expected image_moved error when tag moved without --update, got nil")
	}
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != image.CodeMoved || ue.Kind != client.KindRefused {
		t.Fatalf("expected UserError{Code:%s, Kind:%v}, got %T: %v", image.CodeMoved, client.KindRefused, err, err)
	}
	if !strings.Contains(ue.Msg, "images.lock pins") {
		t.Fatalf("expected image_moved message to mention images.lock pins, got %q", ue.Msg)
	}
	if err := json.Unmarshal([]byte(okJSON(t, "image", "pull", refStr, "box", "--update", "--json")), &pulled); err != nil {
		t.Fatalf("update pull: %v", err)
	}
	if pulled.PlatformDigest != d2 {
		t.Fatalf("updated pull digest = %s, want %s", pulled.PlatformDigest, d2)
	}

	// Mark d2 in use by a session manifest -> image rm refuses with image_in_use.
	sid := host.NewID()
	m := &host.Manifest{
		Schema: 1,
		ID:     sid,
		Name:   "auth-fix",
		Image:  &host.ManifestImage{PlatformDigest: d2, Digest: d2},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(paths.SessionDir(sid)) }()
	fails(t, "auth-fix", "image", "rm", d2[:18], "box")

	// Unused d1 removes cleanly.
	rmSaid := okSaid(t, "image", "rm", d1[:18], "box")
	if !strings.Contains(rmSaid, "Removed") {
		t.Errorf("image rm output = %q", rmSaid)
	}
}

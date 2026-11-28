package image

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// PullOptions configures registry access for Resolve and Fetch.
type PullOptions struct {
	// Keychain provides registry credentials. When nil, Fetch uses anonymous
	// access (host pulls never carry credentials; spec 20.2 step 2).
	Keychain authn.Keychain
	// Transport overrides the HTTP round-tripper (used in tests).
	Transport http.RoundTripper
	// MaxManifestBytes caps manifest and index payloads (default 4 MiB).
	MaxManifestBytes int64
	// Insecure allows plain HTTP registries (used only in local tests).
	Insecure bool
}

// Resolved holds the pinned digest of an OCI reference.
type Resolved struct {
	Ref       string
	Digest    string
	Platforms map[string]string
}

// Resolve resolves refStr to a pinned sha256 digest using go-containerregistry.
// "default" returns the pinned official buildpack-deps:trixie-scm digest with no
// network call. A reference that already carries @sha256:... returns that digest
// without contacting the registry (spec 20.8).
func Resolve(ctx context.Context, refStr string, opts PullOptions) (*Resolved, error) {
	canonical, pinned, isDefault := ResolveDefault(refStr)
	if isDefault {
		plats := make(map[string]string, len(DefaultPlatforms))
		for arch, p := range DefaultPlatforms {
			plats[arch] = p.PlatformDigest
		}
		return &Resolved{Ref: canonical, Digest: pinned, Platforms: plats}, nil
	}

	ref, err := parseRef(canonical, opts.Insecure)
	if err != nil {
		return nil, badLayer(canonical, fmt.Sprintf("invalid image reference: %v", err), err)
	}
	if d, ok := ref.(name.Digest); ok {
		if _, err := ParseSHA256(d.DigestStr()); err != nil {
			return nil, digestMismatch(canonical, err.Error(), err)
		}
		return &Resolved{Ref: ref.String(), Digest: d.DigestStr()}, nil
	}

	desc, err := remote.Get(ref, remoteOpts(ctx, canonical, opts)...)
	if err != nil {
		return nil, classifyRemoteError(canonical, err)
	}
	maxManifest := opts.MaxManifestBytes
	if maxManifest <= 0 {
		maxManifest = MaxManifestBytes
	}
	if int64(len(desc.Manifest)) > maxManifest {
		return nil, badLayer(canonical, fmt.Sprintf("manifest exceeds %d bytes", maxManifest), nil)
	}

	res := &Resolved{
		Ref:       ref.String(),
		Digest:    desc.Digest.String(),
		Platforms: collectIndexPlatforms(desc),
	}
	return res, nil
}

func collectIndexPlatforms(desc *remote.Descriptor) map[string]string {
	platforms := make(map[string]string)
	if !desc.MediaType.IsIndex() {
		return platforms
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return platforms
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return platforms
	}
	for _, m := range im.Manifests {
		if m.Platform == nil || m.Platform.OS != "linux" || m.Platform.Architecture == "" {
			continue
		}
		if _, exists := platforms[m.Platform.Architecture]; !exists {
			platforms[m.Platform.Architecture] = m.Digest.String()
		}
	}
	return platforms
}

// Fetch pulls an OCI image strictly by pinned sha256 digest and selects the
// linux/<arch> image manifest. It returns the v1.Image and the resolved
// platform manifest digest.
func Fetch(ctx context.Context, refStr, digest, arch string, opts PullOptions) (v1.Image, string, error) {
	if _, err := ParseSHA256(digest); err != nil {
		return nil, "", digestMismatch(refStr, err.Error(), err)
	}
	if arch == "" {
		arch = "amd64"
	}
	canonical, _, _ := ResolveDefault(refStr)
	ref, err := parseRef(canonical, opts.Insecure)
	if err != nil {
		return nil, "", badLayer(canonical, fmt.Sprintf("invalid image reference: %v", err), err)
	}
	pinnedRef := ref.Context().Digest(digest)

	desc, err := remote.Get(pinnedRef, remoteOpts(ctx, canonical, opts)...)
	if err != nil {
		return nil, "", classifyRemoteError(canonical, err)
	}
	if desc.Digest.String() != digest {
		return nil, "", digestMismatch(canonical, fmt.Sprintf("got manifest %s, expected %s", desc.Digest, digest), nil)
	}
	maxManifest := opts.MaxManifestBytes
	if maxManifest <= 0 {
		maxManifest = MaxManifestBytes
	}
	if int64(len(desc.Manifest)) > maxManifest {
		return nil, "", badLayer(canonical, fmt.Sprintf("manifest exceeds %d bytes", maxManifest), nil)
	}

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, "", badLayer(canonical, "could not read image index", err)
		}
		return selectPlatformFromIndex(idx, canonical, arch, maxManifest)
	}
	if desc.MediaType.IsImage() {
		img, err := desc.Image()
		if err != nil {
			return nil, "", badLayer(canonical, "could not read image manifest", err)
		}
		if err := checkPlatformArch(img, canonical, arch); err != nil {
			return nil, "", err
		}
		return img, digest, nil
	}
	return nil, "", badLayer(canonical, fmt.Sprintf("unsupported manifest media type %q", desc.MediaType), nil)
}

// FetchFromLayout loads an OCI image or index by digest from a local OCI layout
// directory (created with go-containerregistry's layout package).
func FetchFromLayout(layoutDir, digest, arch string) (v1.Image, string, error) {
	if _, err := ParseSHA256(digest); err != nil {
		return nil, "", digestMismatch(layoutDir, err.Error(), err)
	}
	if arch == "" {
		arch = "amd64"
	}
	p, err := layout.FromPath(layoutDir)
	if err != nil {
		return nil, "", badLayer(layoutDir, "could not open OCI layout", err)
	}
	h, err := v1.NewHash(digest)
	if err != nil {
		return nil, "", digestMismatch(layoutDir, err.Error(), err)
	}
	if idx, err := p.ImageIndex(); err == nil {
		img, platDigest, found, ierr := fetchFromLayoutIndex(idx, h, layoutDir, digest, arch)
		if ierr != nil || found {
			return img, platDigest, ierr
		}
	}
	img, err := p.Image(h)
	if err != nil {
		return nil, "", &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("digest %s not found in layout %s", digest, layoutDir),
			Hint:    "Check the digest and try again.",
			Err:     err,
		}
	}
	if err := checkPlatformArch(img, layoutDir, arch); err != nil {
		return nil, "", err
	}
	return img, digest, nil
}

func fetchFromLayoutIndex(idx v1.ImageIndex, h v1.Hash, layoutDir, digest, arch string) (v1.Image, string, bool, error) {
	if idxDigest, err := idx.Digest(); err == nil && idxDigest == h {
		img, plat, err := selectPlatformFromIndex(idx, layoutDir, arch, MaxManifestBytes)
		return img, plat, true, err
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, "", true, badLayer(layoutDir, "could not decode layout index", err)
	}
	for _, m := range im.Manifests {
		if m.Digest != h {
			continue
		}
		if m.MediaType.IsIndex() {
			sub, err := idx.ImageIndex(h)
			if err != nil {
				return nil, "", true, badLayer(layoutDir, "could not read nested index", err)
			}
			img, plat, err := selectPlatformFromIndex(sub, layoutDir, arch, MaxManifestBytes)
			return img, plat, true, err
		}
		img, err := idx.Image(h)
		if err != nil {
			return nil, "", true, badLayer(layoutDir, "could not read image from layout", err)
		}
		if err := checkPlatformArch(img, layoutDir, arch); err != nil {
			return nil, "", true, err
		}
		return img, digest, true, nil
	}
	return nil, "", false, nil
}

func selectPlatformFromIndex(idx v1.ImageIndex, ref, arch string, maxManifest int64) (v1.Image, string, error) {
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, "", badLayer(ref, "could not decode image index", err)
	}
	var match *v1.Descriptor
	for i := range im.Manifests {
		m := &im.Manifests[i]
		if m.Platform == nil {
			continue
		}
		if m.Platform.OS == "linux" && m.Platform.Architecture == arch {
			match = m
			break
		}
	}
	if match == nil {
		return nil, "", &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("%s has no linux/%s manifest", refLabel(ref), arch),
			Hint:    "Use an image that supports linux/" + arch + ".",
		}
	}
	if match.Size > maxManifest {
		return nil, "", badLayer(ref, fmt.Sprintf("platform manifest exceeds %d bytes", maxManifest), nil)
	}
	img, err := idx.Image(match.Digest)
	if err != nil {
		return nil, "", classifyRemoteError(ref, err)
	}
	gotDigest, err := img.Digest()
	if err != nil {
		return nil, "", badLayer(ref, "could not hash platform manifest", err)
	}
	if gotDigest != match.Digest {
		return nil, "", digestMismatch(ref, fmt.Sprintf("platform manifest %s does not match %s", gotDigest, match.Digest), nil)
	}
	return img, match.Digest.String(), nil
}

func checkPlatformArch(img v1.Image, ref, wantArch string) error {
	cfg, err := img.ConfigFile()
	if err != nil {
		return classifyRemoteError(ref, err)
	}
	if cfg == nil {
		return nil
	}
	if cfg.OS != "" && cfg.OS != "linux" {
		return badLayer(ref, fmt.Sprintf("image OS is %q, expected linux", cfg.OS), nil)
	}
	if cfg.Architecture != "" && cfg.Architecture != wantArch {
		return &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("%s is for linux/%s, not linux/%s", refLabel(ref), cfg.Architecture, wantArch),
			Hint:    "Use an image built for linux/" + wantArch + ".",
		}
	}
	return nil
}

func parseRef(refStr string, insecure bool) (name.Reference, error) {
	var opts []name.Option
	if insecure {
		opts = append(opts, name.Insecure)
	}
	ref, err := name.ParseReference(refStr, opts...)
	if err != nil {
		return nil, fmt.Errorf("parse reference %q: %w", refStr, err)
	}
	return ref, nil
}

func remoteOpts(ctx context.Context, ref string, opts PullOptions) []remote.Option {
	ro := []remote.Option{remote.WithContext(ctx)}
	if opts.Keychain != nil {
		ro = append(ro, remote.WithAuthFromKeychain(opts.Keychain))
	} else {
		ro = append(ro, remote.WithAuth(authn.Anonymous))
	}
	base := opts.Transport
	if base == nil {
		base = remote.DefaultTransport
	}
	maxManifest := opts.MaxManifestBytes
	if maxManifest <= 0 {
		maxManifest = MaxManifestBytes
	}
	ro = append(ro, remote.WithTransport(&manifestLimitTransport{
		base:  base,
		limit: maxManifest,
		ref:   ref,
	}))
	return ro
}

func classifyRemoteError(ref string, err error) error {
	if ie, ok := AsError(err); ok {
		return ie
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{
		Code:    CodeBadLayer,
		Message: fmt.Sprintf("could not fetch %s: %v", refLabel(ref), err),
		Hint:    "Check the image name, your network and registry access, then try again.",
		Err:     err,
	}
}

// ParseSHA256 validates "sha256:<64 lowercase hex>" and returns the hex part.
func ParseSHA256(digest string) (string, error) {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return "", fmt.Errorf("digest %q must have a sha256 prefix", digest)
	}
	if len(hexPart) != 64 {
		return "", fmt.Errorf("digest %q must have 64 hex characters", digest)
	}
	if hexPart != strings.ToLower(hexPart) {
		return "", fmt.Errorf("digest %q must be lowercase hex", digest)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("digest %q is not valid hex", digest)
	}
	return hexPart, nil
}

type manifestLimitTransport struct {
	base  http.RoundTripper
	limit int64
	ref   string
}

func (t *manifestLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("registry request: %w", err)
	}
	if !isManifestRequest(req, resp) {
		return resp, nil
	}
	if resp.ContentLength > t.limit {
		_ = resp.Body.Close()
		return nil, badLayer(t.ref, fmt.Sprintf("manifest is %d bytes, over the %d byte limit", resp.ContentLength, t.limit), nil)
	}
	resp.Body = &manifestLimitReader{rc: resp.Body, limit: t.limit, ref: t.ref}
	return resp, nil
}

func isManifestRequest(req *http.Request, resp *http.Response) bool {
	if req != nil && req.URL != nil && strings.Contains(req.URL.Path, "/manifests/") {
		return true
	}
	if resp != nil {
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.Contains(ct, "manifest") || strings.Contains(ct, "image.index") {
			return true
		}
	}
	return false
}

type manifestLimitReader struct {
	rc    io.ReadCloser
	read  int64
	limit int64
	ref   string
}

func (r *manifestLimitReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	r.read += int64(n)
	if r.read > r.limit {
		return 0, badLayer(r.ref, fmt.Sprintf("manifest exceeds %d bytes", r.limit), nil)
	}
	if err != nil {
		return n, err //nolint:wrapcheck // preserves io.EOF for HTTP body readers
	}
	return n, nil
}

func (r *manifestLimitReader) Close() error {
	if err := r.rc.Close(); err != nil {
		return fmt.Errorf("close manifest body: %w", err)
	}
	return nil
}

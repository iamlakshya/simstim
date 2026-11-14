package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

// Credentials belong to one registry and never enter a host request.
type Credentials struct{ Username, Password, IdentityToken string }

// Registry reads bounded OCI documents and blobs. Host callers leave Auth empty.
type Registry struct {
	Client *http.Client
	Auth   Credentials
	mu     sync.Mutex
	tokens map[string]string
}

// Document is one digest-checked manifest or index.
type Document struct {
	Digest string
	Data   []byte
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

func (r *Registry) client() *http.Client {
	base := r.Client
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.User != nil {
			return badLayer("registry redirects must use HTTPS without URL credentials")
		}
		if base.CheckRedirect != nil {
			return base.CheckRedirect(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("registry redirected more than ten times")
		}
		return nil
	}
	return &copy
}

func (r *Registry) get(ctx context.Context, ref Reference, kind, id string) (*http.Response, error) {
	u := "https://" + ref.Registry + "/v2/" + ref.Repository + "/" + kind + "/" + url.PathEscape(id)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if kind == "manifests" {
		request.Header.Set("Accept", manifestAccept)
	} else {
		request.Header.Set("Accept", "*/*")
	}
	key := ref.Registry + "/" + ref.Repository
	r.mu.Lock()
	token := r.tokens[key]
	r.mu.Unlock()
	if r.Auth.Username != "" || r.Auth.Password != "" {
		request.SetBasicAuth(r.Auth.Username, r.Auth.Password)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := r.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		challenge := response.Header.Get("WWW-Authenticate")
		response.Body.Close()
		token, err := r.bearer(ctx, challenge, ref)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		if r.tokens == nil {
			r.tokens = map[string]string{}
		}
		r.tokens[key] = token
		r.mu.Unlock()
		request = request.Clone(ctx)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err = r.client().Do(request)
		if err != nil {
			return nil, fmt.Errorf("read registry: %w", err)
		}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("registry refused image request (HTTP %d)", response.StatusCode)
	}
	return response, nil
}

// Manifest reads no more than 4 MiB and verifies a supplied digest locally.
func (r *Registry) Manifest(ctx context.Context, ref Reference, id string) (Document, error) {
	response, err := r.get(ctx, ref, "manifests", id)
	if err != nil {
		return Document{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxManifest+1))
	if err != nil {
		return Document{}, fmt.Errorf("read image manifest: %w", err)
	}
	if int64(len(data)) > MaxManifest {
		return Document{}, badLayer("image manifest exceeds 4 MiB")
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if ValidDigest(id) && digest != id {
		return Document{}, mismatch("image manifest")
	}
	return Document{digest, data}, nil
}

func parseManifest(doc Document) (manifest, error) {
	var m manifest
	if int64(len(doc.Data)) > MaxManifest {
		return m, badLayer("image manifest exceeds 4 MiB")
	}
	sum := sha256.Sum256(doc.Data)
	if "sha256:"+hex.EncodeToString(sum[:]) != doc.Digest {
		return m, mismatch("image manifest")
	}
	if err := json.Unmarshal(doc.Data, &m); err != nil {
		return m, badLayer("invalid image manifest: %v", err)
	}
	if m.SchemaVersion != 2 {
		return m, badLayer("image manifest has an unsupported schema")
	}
	return m, nil
}

// Platform validates an index's unique linux architecture descriptor.
func Platform(doc Document, arch string) (string, error) {
	m, err := parseManifest(doc)
	if err != nil {
		return "", err
	}
	switch m.MediaType {
	case "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
		return doc.Digest, nil
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
	default:
		return "", badLayer("unsupported manifest media type %q", m.MediaType)
	}
	selected := ""
	for _, d := range m.Manifests {
		if d.Platform.OS == "linux" && d.Platform.Architecture == arch && (d.Platform.Variant == "" || arch == "arm64" && d.Platform.Variant == "v8") {
			if selected != "" || !ValidDigest(d.Digest) || d.Size <= 0 || d.Size > MaxManifest {
				return "", badLayer("image index has an invalid or ambiguous platform")
			}
			selected = d.Digest
		}
	}
	if selected == "" {
		return "", badLayer("image index has no linux/%s manifest", arch)
	}
	return selected, nil
}

// Layers validates all advertised sizes before any download or conversion.
func Layers(doc Document) ([]Layer, error) {
	m, err := parseManifest(doc)
	if err != nil {
		return nil, err
	}
	if m.MediaType != "application/vnd.oci.image.manifest.v1+json" && m.MediaType != "application/vnd.docker.distribution.manifest.v2+json" {
		return nil, badLayer("platform digest does not name an image manifest")
	}
	if len(m.Layers) > MaxLayers {
		return nil, tooLarge("image has more than 128 layers")
	}
	var total int64
	layers := make([]Layer, 0, len(m.Layers))
	for _, d := range m.Layers {
		if !ValidDigest(d.Digest) || d.Size < 0 {
			return nil, badLayer("image layer has invalid digest or size")
		}
		if d.Size > MaxCompressed-total {
			return nil, tooLarge("image compressed layers exceed 8 GiB")
		}
		total += d.Size
		layers = append(layers, Layer{d.Digest, d.MediaType, d.Size})
	}
	return layers, nil
}

// Resolve chooses a platform using laptop registry bytes and authentication.
func (r *Registry) Resolve(ctx context.Context, text, arch string) (Request, []Document, []Layer, error) {
	ref, err := ParseReference(text)
	if err != nil {
		return Request{}, nil, nil, err
	}
	doc, err := r.Manifest(ctx, ref, ref.Identifier)
	if err != nil {
		return Request{}, nil, nil, err
	}
	platform, err := Platform(doc, arch)
	if err != nil {
		return Request{}, nil, nil, err
	}
	docs := []Document{doc}
	if platform != doc.Digest {
		doc, err = r.Manifest(ctx, ref, platform)
		if err != nil {
			return Request{}, nil, nil, err
		}
		docs = append(docs, doc)
	}
	layers, err := Layers(doc)
	return Request{Ref: text, Digest: docs[0].Digest, PlatformDigest: platform, Arch: arch}, docs, layers, err
}

// Blob streams exactly the advertised compressed size and checks SHA-256.
func (r *Registry) Blob(ctx context.Context, ref Reference, layer Layer, out io.Writer, progress func(int64)) error {
	if !ValidDigest(layer.Digest) || layer.Size < 0 || layer.Size > MaxCompressed {
		return badLayer("invalid layer descriptor")
	}
	response, err := r.get(ctx, ref, "blobs", layer.Digest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	h := sha256.New()
	writer := io.MultiWriter(out, h)
	var n int64
	buf := make([]byte, MaxChunk)
	for n < layer.Size {
		want := int64(len(buf))
		if layer.Size-n < want {
			want = layer.Size - n
		}
		read, e := io.ReadFull(response.Body, buf[:want])
		if read > 0 {
			if _, err := writer.Write(buf[:read]); err != nil {
				return err
			}
			n += int64(read)
			if progress != nil {
				progress(n)
			}
		}
		if e != nil {
			return mismatch("image layer size")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var extra [1]byte
	if count, e := response.Body.Read(extra[:]); count != 0 || e != io.EOF {
		return mismatch("image layer size")
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.TrimPrefix(layer.Digest, "sha256:") {
		return mismatch("image layer")
	}
	return nil
}

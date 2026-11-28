package host

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, "images") }

func (b *bridge) handleImages(req proto.Request) bool {
	switch req.Op {
	case "image.ls":
		list, err := ListImages()
		b.reply(req.ID, list, imageProtoError(err))
	case "image.pull":
		var a api.ImagePullArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the image.pull request is not valid JSON", "Update the client."))
			return true
		}
		rec, err := PullImage(context.Background(), a)
		b.reply(req.ID, rec, imageProtoError(err))
	case "image.rm":
		var a api.ImageRemoveArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the image.rm request is not valid JSON", "Update the client."))
			return true
		}
		rec, err := RemoveImage(a.Digest)
		b.reply(req.ID, rec, imageProtoError(err))
	case "image.agents":
		rec, err := BuildAgentsDisk(context.Background())
		b.reply(req.ID, rec, imageProtoError(err))
	case "image.agents.ensure":
		rec, _, err := EnsureAgentsDisk(context.Background())
		b.reply(req.ID, rec, imageProtoError(err))
	default:
		return false
	}
	return true
}

func imageProtoError(err error) error {
	if err == nil {
		return nil
	}
	if ie, ok := image.AsError(err); ok {
		return UserError(ie.Code, ie.Message, ie.Hint)
	}
	return err
}

// ImageStore opens this host's content-addressed image store.
func ImageStore() *image.Store {
	return image.NewStore(paths.ImagesDir())
}

// ListImages returns every converted session image stored on this host.
func ListImages() ([]api.ImageRecord, error) {
	list, err := ImageStore().List()
	if err != nil {
		return nil, imageProtoError(err)
	}
	if list == nil {
		return []api.ImageRecord{}, nil
	}
	return list, nil
}

// PullImage fetches a pinned OCI image by digest (with anonymous registry
// access on the host), flattens it with go-containerregistry, and converts it
// into the host's store.
func PullImage(ctx context.Context, a api.ImagePullArgs) (api.ImageRecord, error) {
	arch := a.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	rec, err := ImageStore().Pull(ctx, image.PullRequest{
		Ref:              a.Ref,
		Digest:           a.Digest,
		Arch:             arch,
		ExpectedRootHash: a.ExpectedRootHash,
	})
	return rec, imageProtoError(err)
}

// RemoveImage deletes one stored image unless a non-deleted session still uses
// it.
func RemoveImage(digest string) (api.ImageRecord, error) {
	rec, err := ImageStore().Remove(digest, sessionUsingImage)
	return rec, imageProtoError(err)
}

// BuildAgentsDisk builds the read-only agents disk (EROFS via mkfs.erofs, or
// pure-Go ext4 via tar2ext4 when erofs-utils is absent) from the agents
// currently installed on this host.
func BuildAgentsDisk(ctx context.Context) (api.AgentsDiskRecord, error) {
	sources := InstalledAgentSources(ctx)
	rec, err := ImageStore().BuildAgentsDisk(ctx, sources, hostAgentsDiskOptions())
	return rec, imageProtoError(err)
}

// PruneImages removes unreferenced, expired images and old agents disks from
// the host store.
func PruneImages() ([]string, error) {
	imgs, agentDisks := sessionImageRefs()
	return ImageStore().Prune(image.PruneKeep{
		SessionImages: imgs,
		DefaultImages: image.DefaultPinnedHexes(),
		SessionAgents: agentDisks,
	})
}

func sessionUsingImage(rec api.ImageRecord) string {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := LoadManifest(e.Name())
		if err != nil || m.Image == nil {
			continue
		}
		if sameDigest(m.Image.PlatformDigest, rec.PlatformDigest) ||
			sameDigest(m.Image.Digest, rec.PlatformDigest) ||
			sameDigest(m.Image.Digest, rec.Digest) {
			if m.Name != "" {
				return m.Name
			}
			return m.ID
		}
	}
	return ""
}

func sessionImageRefs() (images, agentDisks []string) {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := LoadManifest(e.Name())
		if err != nil {
			continue
		}
		if m.Image != nil {
			if m.Image.PlatformDigest != "" {
				images = append(images, m.Image.PlatformDigest)
			}
			if m.Image.Digest != "" {
				images = append(images, m.Image.Digest)
			}
		}
		if m.Agents != "" {
			agentDisks = append(agentDisks, m.Agents)
		}
	}
	return images, agentDisks
}

func sameDigest(a, b string) bool {
	na := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(a), "sha256:"))
	nb := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(b), "sha256:"))
	return na != "" && na == nb
}

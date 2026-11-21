package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/image"
)

// ImageRegistry reads the laptop's own registry login. The returned registry
// may fetch bytes locally; its credentials must never enter a host request.
func ImageRegistry(ctx context.Context, text string) (*image.Registry, error) {
	if err := imageRegistryPolicyBoundary(ctx); err != nil {
		return nil, err
	}
	ref, err := image.ParseReference(text)
	if err != nil {
		return nil, err
	}
	credentials, err := imageCredentials(ctx, ref.Registry)
	if err != nil {
		return nil, err
	}
	if err := imageRegistryPolicyBoundary(ctx); err != nil {
		return nil, err
	}
	return &image.Registry{Auth: credentials}, nil
}

func imageRegistryPolicyBoundary(ctx context.Context) error {
	if original, _ := ctx.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1); original != nil {
		return revalidateRetainedImagePolicy(ctx)
	}
	return ctx.Err()
}

func imageCredentials(ctx context.Context, registry string) (image.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return image.Credentials{}, err
	}
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return image.Credentials{}, err
		}
		dir = filepath.Join(home, ".docker")
	}
	file, err := openImageCredentialConfig(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return image.Credentials{}, nil
	}
	if err != nil {
		return image.Credentials{}, fmt.Errorf("read registry login: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return image.Credentials{}, err
	}
	if info.Size() > image.MaxManifest {
		return image.Credentials{}, fmt.Errorf("registry login file exceeds 4 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(imageCredentialReader{ctx: ctx, file: file}, image.MaxManifest+1))
	if err != nil {
		return image.Credentials{}, err
	}
	if int64(len(data)) > image.MaxManifest {
		return image.Credentials{}, fmt.Errorf("registry login file exceeds 4 MiB")
	}
	var config struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return image.Credentials{}, fmt.Errorf("read registry login: %w", err)
	}
	server := registry
	if registry == "registry-1.docker.io" {
		server = "https://index.docker.io/v1/"
	}
	helper := config.CredHelpers[registry]
	if registry == "registry-1.docker.io" && helper == "" {
		for _, key := range []string{server, "docker.io", "index.docker.io"} {
			if helper = config.CredHelpers[key]; helper != "" {
				break
			}
		}
	}
	if helper == "" {
		helper = config.CredsStore
	}
	if helper != "" {
		if strings.ContainsAny(helper, "/\\\x00\r\n") {
			return image.Credentials{}, fmt.Errorf("registry credential helper has an invalid name")
		}
		if err := ctx.Err(); err != nil {
			return image.Credentials{}, err
		}
		if err := imageRegistryPolicyBoundary(ctx); err != nil {
			return image.Credentials{}, err
		}
		helperContext, cancel := context.WithCancel(ctx)
		defer cancel()
		command := exec.CommandContext(helperContext, "docker-credential-"+helper, "get")
		command.WaitDelay = sshWaitDelay
		command.Stdin = strings.NewReader(server + "\n")
		var output bytes.Buffer
		bounded := &imageBoundedBuffer{buffer: &output, remaining: image.MaxManifest, cancel: cancel}
		command.Stdout = bounded
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return image.Credentials{}, ctx.Err()
			}
			if bounded.overflow {
				return image.Credentials{}, fmt.Errorf("registry credential helper response exceeds 4 MiB")
			}
			if strings.Contains(strings.ToLower(output.String()), "credentials not found") {
				return image.Credentials{}, nil
			}
			return image.Credentials{}, fmt.Errorf("read registry credential helper: %w", err)
		}
		if err := imageRegistryPolicyBoundary(ctx); err != nil {
			return image.Credentials{}, err
		}
		var credentials struct{ Username, Secret string }
		if err := json.Unmarshal(output.Bytes(), &credentials); err != nil {
			return image.Credentials{}, fmt.Errorf("read registry credential helper response: %w", err)
		}
		if credentials.Username == "<token>" {
			return image.Credentials{IdentityToken: credentials.Secret}, nil
		}
		return image.Credentials{Username: credentials.Username, Password: credentials.Secret}, nil
	}
	entry, ok := config.Auths[server]
	if !ok {
		entry = config.Auths[registry]
	}
	if registry == "registry-1.docker.io" && entry.Auth == "" && entry.IdentityToken == "" {
		entry = config.Auths["docker.io"]
	}
	if entry.IdentityToken != "" {
		return image.Credentials{IdentityToken: entry.IdentityToken}, nil
	}
	if entry.Auth == "" {
		return image.Credentials{}, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return image.Credentials{}, fmt.Errorf("registry login has invalid base64")
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return image.Credentials{}, fmt.Errorf("registry login has no password separator")
	}
	return image.Credentials{Username: user, Password: password}, nil
}

type imageBoundedBuffer struct {
	buffer    *bytes.Buffer
	remaining int64
	cancel    context.CancelFunc
	overflow  bool
}

func (b *imageBoundedBuffer) Write(p []byte) (int, error) {
	if int64(len(p)) > b.remaining {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, fmt.Errorf("registry credential helper response exceeds 4 MiB")
	}
	b.remaining -= int64(len(p))
	return b.buffer.Write(p)
}

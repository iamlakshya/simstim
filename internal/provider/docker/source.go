package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type dockerSourceLocator struct {
	endpoint, engine string
	config           client.MachineConfigSource
	environment      []string
}

func sourceUnknown() error {
	return &client.UserError{Code: "source_inventory", Msg: "the original Docker resource or sharing configuration cannot be checked", Hint: "Use the original explicit Docker engine and repair its managed creation/configuration record.", Kind: client.KindRefused}
}

func (l dockerSourceLocator) args(args ...string) []string {
	return append([]string{"--host", l.endpoint, "--config", l.config.Path}, args...)
}
func (l dockerSourceLocator) exec(ctx context.Context, args ...string) ([]byte, error) {
	return provider.ExecEnvironment(ctx, l.environment, install, "docker", l.args(args...)...)
}

func captureSourceLocator(ctx context.Context) (dockerSourceLocator, error) {
	var locator dockerSourceLocator
	config := os.Getenv("DOCKER_CONFIG")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return locator, err
		}
		config = filepath.Join(home, ".docker")
	}
	source, err := provider.CaptureSourceConfig(config)
	if err != nil {
		return locator, err
	}
	locator.config = source
	// Host creation retains its existing authorised provider environment, but
	// the three mutable selector variables cannot override the pinned locator.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "DOCKER_CONTEXT") && !strings.EqualFold(key, "DOCKER_HOST") && !strings.EqualFold(key, "DOCKER_CONFIG") {
			locator.environment = append(locator.environment, entry)
		}
	}
	contextName := os.Getenv("DOCKER_CONTEXT")
	endpoint := os.Getenv("DOCKER_HOST")
	if contextName != "" || endpoint == "" {
		if contextName == "" {
			raw, err := provider.ExecEnvironment(ctx, locator.environment, install, "docker", "--config", source.Path, "context", "show")
			if err != nil {
				return locator, err
			}
			contextName = strings.TrimSpace(string(raw))
		}
		raw, err := provider.ExecEnvironment(ctx, locator.environment, install, "docker", "--config", source.Path, "context", "inspect", contextName, "--format", "{{json .Endpoints.docker.Host}}")
		if err != nil || json.Unmarshal(raw, &endpoint) != nil {
			return locator, sourceUnknown()
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return locator, sourceUnknown()
	}
	if parsed.User != nil {
		if _, password := parsed.User.Password(); password {
			return locator, sourceUnknown()
		}
	}
	locator.endpoint = endpoint
	raw, err := locator.exec(ctx, "info", "--format", "{{json .ID}}")
	if err != nil || json.Unmarshal(raw, &locator.engine) != nil || locator.engine == "" {
		return locator, sourceUnknown()
	}
	pin, err := provider.PinSourceConfig(source)
	if err != nil {
		return locator, err
	}
	return locator, pin.Close()
}

type dockerSharing struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Label  string `json:"label"`
	Mounts *[]struct {
		Type   *string `json:"Type"`
		Source *string `json:"Source"`
		RW     *bool   `json:"RW"`
	} `json:"mounts"`
}

func sharingTemplate() string {
	return `{"id":{{json .Id}},"name":{{json .Name}},"label":{{json (index .Config.Labels "` + brand.Name + `.host")}},"mounts":{{json .Mounts}}}`
}

// InspectSource reads the ORIGINAL actual engine/resource and complete current
// effective mount data. It never treats a legacy tuple or current default as a
// no-sharing fact. Unknown laptop path projection is a refusal.
func InspectSource(ctx context.Context, h client.Host, environment []string) (_ *provider.SourceInspection, err error) {
	if h.Machine == nil {
		return nil, sourceUnknown()
	}
	source, err := client.NormaliseMachineSource("docker", h.Machine.Source)
	if err != nil {
		return nil, err
	}
	endpoint, parseErr := url.Parse(source.Endpoint)
	if parseErr != nil || endpoint.Scheme != "unix" && endpoint.Scheme != "npipe" {
		return nil, sourceUnknown()
	}
	if source.Config.Role == client.MachineConfigUnused {
		return nil, sourceUnknown()
	}
	pin, err := provider.PinSourceConfig(source.Config)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, pin.Close())
		}
	}()
	prefix := []string{"--host", source.Endpoint, "--config", source.Config.Path}
	read := func(args ...string) ([]byte, error) {
		return provider.ExecMetadata(ctx, environment, 4<<20, install, "docker", append(append([]string(nil), prefix...), args...)...)
	}
	raw, err := read("info", "--format", "{{json .ID}}")
	var engine string
	if err != nil || json.Unmarshal(raw, &engine) != nil || engine != source.EngineID {
		return nil, sourceUnknown()
	}
	raw, err = read("container", "inspect", source.ResourceID, "--format", sharingTemplate())
	if err != nil {
		return nil, err
	}
	var sharing dockerSharing
	if json.Unmarshal(raw, &sharing) != nil || sharing.ID != source.ResourceID || sharing.Name != "/"+h.Machine.ID || sharing.Label != h.Name || sharing.Mounts == nil {
		return nil, sourceUnknown()
	}
	inspection := &provider.SourceInspection{Config: pin}
	for _, mount := range *sharing.Mounts {
		if mount.Type == nil || mount.Source == nil || mount.RW == nil {
			return nil, sourceUnknown()
		}
		if *mount.Type == "tmpfs" && *mount.Source == "" {
			continue
		}
		if *mount.Type != "bind" && *mount.Type != "volume" {
			return nil, sourceUnknown()
		}
		if !filepath.IsAbs(*mount.Source) || filepath.Clean(*mount.Source) != *mount.Source {
			return nil, sourceUnknown()
		}
		if _, statErr := os.Lstat(*mount.Source); statErr != nil {
			return nil, sourceUnknown()
		}
		// Even a top-level RO bind can have writable recursive submounts.
		// Restricting the whole actual source is a conservative superset.
		inspection.Paths = append(inspection.Paths, *mount.Source)
	}
	if err := pin.Revalidate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	inspection.Stamp = hex.EncodeToString(sum[:]) + ":" + source.EngineID
	return inspection, nil
}

func (l dockerSourceLocator) receipt(ctx context.Context, h client.Host, id string) (*client.MachineSourceProvenance, error) {
	source := &client.MachineSourceProvenance{Version: client.MachineSourceVersion, Kind: "docker-engine", ResourceID: strings.TrimSpace(id), Endpoint: l.endpoint, EngineID: l.engine, Config: l.config}
	if _, err := client.NormaliseMachineSource("docker", source); err != nil {
		return nil, err
	}
	h.Machine.Source = source
	engineRaw, err := l.exec(ctx, "info", "--format", "{{json .ID}}")
	var engine string
	if err != nil || json.Unmarshal(engineRaw, &engine) != nil || engine != l.engine {
		return nil, sourceUnknown()
	}
	raw, err := l.exec(ctx, "container", "inspect", source.ResourceID, "--format", sharingTemplate())
	if err != nil {
		return nil, err
	}
	var sharing dockerSharing
	if json.Unmarshal(raw, &sharing) != nil || sharing.ID != source.ResourceID || sharing.Name != "/"+h.Machine.ID || sharing.Label != h.Name || sharing.Mounts == nil {
		return nil, sourceUnknown()
	}
	pin, err := provider.PinSourceConfig(source.Config)
	if err != nil {
		return nil, err
	}
	return source, pin.Close()
}

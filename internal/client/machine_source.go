package client

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// MachineSourceVersion is the closed managed-resource provenance format.
const MachineSourceVersion uint32 = 1

// MachineConfigRole records the actual configuration source used at creation.
// It is not a sharing permission or a replacement for current inspection.
type MachineConfigRole string

const (
	// MachineConfigExisting identifies an observed existing source object.
	MachineConfigExisting MachineConfigRole = "existing"
	// MachineConfigAbsent identifies an actual selected path absent at creation.
	MachineConfigAbsent MachineConfigRole = "absent"
	// MachineConfigUnused records an explicit direct local endpoint consuming no configuration source.
	MachineConfigUnused MachineConfigRole = "unused"
)

// MachineConfigSource is a closed existing/absent/unused source union. Current
// readers reacquire native object/parent/absence pins; these strings grant none.
type MachineConfigSource struct {
	Role     MachineConfigRole `toml:"role"`
	Path     string            `toml:"path,omitempty"`
	ObjectID string            `toml:"object_id,omitempty"`
	ParentID string            `toml:"parent_id,omitempty"`
}

// MachineSourceProvenance binds only the original closed creation's resource
// and locator. Fresh complete sharing/configuration evidence stays ephemeral.
// The closed creation writer is the sole producer of a saved new value.
type MachineSourceProvenance struct {
	Version    uint32              `toml:"version"`
	Kind       string              `toml:"kind"`
	ResourceID string              `toml:"resource_id"`
	Endpoint   string              `toml:"endpoint,omitempty"`
	EngineID   string              `toml:"engine_id,omitempty"`
	Config     MachineConfigSource `toml:"config"`
}

// CloneMachineSource returns an independent copy of immutable source data.
// It neither inspects a provider nor refreshes the original source identity.
func CloneMachineSource(source *MachineSourceProvenance) *MachineSourceProvenance {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func machineSourceUnknown() error {
	return &UserError{Code: "source_inventory", Msg: "the managed machine has no usable original source provenance",
		Hint: "Use a host created through the current closed provider flow with verified local sharing configuration.", Kind: KindRefused}
}

func machineSourceText(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

// NormaliseMachineSource validates one relevant local machine's stored shape.
// This is data validation only: a caller must still inspect the ORIGINAL actual
// engine/resource/configuration completely before adding source restrictions.
// Missing, zero and future formats never authorise a provider command or read.
func NormaliseMachineSource(provider string, source *MachineSourceProvenance) (*MachineSourceProvenance, error) {
	if source == nil || source.Version != MachineSourceVersion || !machineSourceText(source.ResourceID) {
		return nil, machineSourceUnknown()
	}
	if err := checkMachineConfigSource(source.Config); err != nil {
		return nil, err
	}
	switch provider {
	case "lima":
		if source.Kind != "lima-plain" || source.Endpoint != "" || source.EngineID != "" || source.Config.Role != MachineConfigExisting {
			return nil, machineSourceUnknown()
		}
	case "docker":
		if source.Kind != "docker-engine" || len(source.ResourceID) != 64 || !api.ValidGitOID(source.ResourceID) ||
			!machineSourceText(source.EngineID) || !machineSourceText(source.Endpoint) {
			return nil, machineSourceUnknown()
		}
		endpoint, err := url.Parse(source.Endpoint)
		if err != nil || endpoint.Scheme == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return nil, machineSourceUnknown()
		}
		if endpoint.User != nil {
			if _, password := endpoint.User.Password(); password {
				return nil, machineSourceUnknown()
			}
		}
		if source.Config.Role == MachineConfigUnused && endpoint.Scheme != "unix" && endpoint.Scheme != "npipe" {
			return nil, machineSourceUnknown()
		}
	default:
		return nil, machineSourceUnknown()
	}
	return CloneMachineSource(source), nil
}

func checkMachineConfigSource(source MachineConfigSource) error {
	if source.Role == MachineConfigUnused {
		if source.Path != "" || source.ObjectID != "" || source.ParentID != "" {
			return machineSourceUnknown()
		}
		return nil
	}
	if !machineSourceText(source.Path) || !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path {
		return machineSourceUnknown()
	}
	switch source.Role {
	case MachineConfigExisting:
		if !machineSourceText(source.ObjectID) || source.ParentID != "" {
			return machineSourceUnknown()
		}
	case MachineConfigAbsent:
		if !machineSourceText(source.ParentID) || source.ObjectID != "" || filepath.Dir(source.Path) == source.Path {
			return machineSourceUnknown()
		}
	default:
		return machineSourceUnknown()
	}
	return nil
}

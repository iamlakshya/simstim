package guest

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DecoyPayloadMax bounds the complete private file payload before decoding.
const DecoyPayloadMax = proto.DecoyPayloadMax

// DecoyValueFormat is the single shared decoy data format.
type DecoyValueFormat = proto.DecoyValueFormat

// DecoyDefinition is the single shared inert file definition.
type DecoyDefinition = proto.DecoyDefinition

// DecoyDefinitions returns owned copies from the one embedded data table.
func DecoyDefinitions() ([]DecoyDefinition, error) { return proto.DecoyDefinitions() }

// DecoyPaths returns the shared home-relative image exclusion list.
func DecoyPaths() ([]string, error) { return proto.DecoyPaths() }

// RenderDecoy renders the one shared template with the checked fake values.
func RenderDecoy(name string, values []api.Secret) (api.Secret, error) {
	raw := make([]string, len(values))
	for index := range values {
		raw[index] = string(values[index])
	}
	content, err := proto.RenderDecoy(name, raw)
	return api.Secret(content), err
}

// ValidateDecoyFilesV1 checks every file before the guest writes any name.
func ValidateDecoyFilesV1(input *proto.DecoyFilesV1) error { return proto.ValidateDecoyFilesV1(input) }

func validateDecoyContent(definition DecoyDefinition, content string) bool {
	return proto.ValidateDecoyContent(definition.Name, content) == nil
}

func decoyFailure(message string) error {
	return &proto.Error{Code: "sandbox_boot", Message: message, Hint: "Start the sandbox again with matching helper files; keep decoys disabled if its home cannot hold the fixed files."}
}

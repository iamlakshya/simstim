package proto

import (
	_ "embed"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// DecoyPayloadMax bounds the complete private file payload, before decoding.
const DecoyPayloadMax = 16 << 10

//go:embed decoys.toml
var decoyData string

// DecoyValueFormat is one scanner-compatible generated field in a data row.
// Length counts the complete value, including Prefix. Alphabet is ASCII.
type DecoyValueFormat struct {
	Name     string `toml:"name"`
	Prefix   string `toml:"prefix"`
	Length   int    `toml:"length"`
	Alphabet string `toml:"alphabet"`
}

// DecoyDefinition is public inert file data, never a generated credential.
type DecoyDefinition struct {
	Name     string             `toml:"name"`
	Path     string             `toml:"path"`
	Template string             `toml:"template"`
	Sources  []string           `toml:"sources"`
	Hosts    []string           `toml:"hosts"`
	Values   []DecoyValueFormat `toml:"values"`
}

func decoyFailure(message string) error {
	return &Error{Code: "sandbox_boot", Message: message, Hint: "Start the sandbox again with matching helper files; keep decoys disabled if its home cannot hold the fixed files."}
}

func decoyName(name string) bool {
	if name == "" || len(name) > 32 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

var canonicalDecoys = sync.OnceValues(func() ([]DecoyDefinition, error) {
	var data struct {
		Version int               `toml:"version"`
		Decoys  []DecoyDefinition `toml:"decoy"`
	}
	metadata, err := toml.Decode(decoyData, &data)
	if err != nil || len(metadata.Undecoded()) != 0 || data.Version != 1 || len(data.Decoys) == 0 || len(data.Decoys) > 16 {
		return nil, decoyFailure("the embedded decoy data is invalid")
	}
	seen, paths := map[string]bool{}, map[string]bool{}
	var total int
	for _, definition := range data.Decoys {
		if !decoyName(definition.Name) || seen[definition.Name] || paths[definition.Path] || (!fs.ValidPath(definition.Path) || strings.ContainsAny(definition.Path, "\\\x00\r\n")) || len(definition.Sources) == 0 || len(definition.Hosts) == 0 || len(definition.Hosts) > 16 || len(definition.Template) > 8192 || len(definition.Values) == 0 || len(definition.Values) > 8 {
			return nil, decoyFailure("the embedded decoy file declaration is invalid")
		}
		seen[definition.Name], paths[definition.Path] = true, true
		for _, source := range definition.Sources {
			if !strings.HasPrefix(source, "https://") || strings.ContainsAny(source, " \t\r\n\x00") {
				return nil, decoyFailure("the embedded decoy source link is invalid")
			}
		}
		for _, host := range definition.Hosts {
			if host == "" || strings.ContainsAny(host, "/:@ \\?\r\n") || strings.ToLower(host) != host {
				return nil, decoyFailure("the embedded decoy host class is invalid")
			}
		}
		fields := map[string]bool{}
		remainder := definition.Template
		total += len(definition.Template)
		for _, value := range definition.Values {
			marker := "{{" + value.Name + "}}"
			if !decoyName(value.Name) || fields[value.Name] || strings.Count(definition.Template, marker) != 1 || value.Length <= len(value.Prefix) || value.Length > 128 || len(value.Alphabet) < 2 || len(value.Alphabet) > 128 {
				return nil, decoyFailure("the embedded decoy value format is invalid")
			}
			fields[value.Name] = true
			characters := map[byte]bool{}
			for _, character := range []byte(value.Alphabet) {
				if character <= 0x20 || character >= 0x7f || characters[character] {
					return nil, decoyFailure("the embedded decoy alphabet is invalid")
				}
				characters[character] = true
			}
			before, after, found := strings.Cut(remainder, marker)
			if !found || strings.ContainsAny(before, "{}") || strings.ContainsAny(value.Prefix, "\x00\r\n") {
				return nil, decoyFailure("the embedded decoy template is invalid")
			}
			remainder = after
			total += value.Length - len(marker)
		}
		if strings.ContainsAny(remainder, "{}") || total > DecoyPayloadMax/2 || path.IsAbs(definition.Path) {
			return nil, decoyFailure("the embedded decoy template exceeds its bound")
		}
	}
	return data.Decoys, nil
})

// DecoyDefinitions returns owned copies of the single embedded data table.
func DecoyDefinitions() ([]DecoyDefinition, error) {
	definitions, err := canonicalDecoys()
	if err != nil {
		return nil, err
	}
	output := slices.Clone(definitions)
	for index := range output {
		output[index].Sources = slices.Clone(definitions[index].Sources)
		output[index].Hosts = slices.Clone(definitions[index].Hosts)
		output[index].Values = slices.Clone(definitions[index].Values)
	}
	return output, nil
}

// DecoyPaths is the canonical home-relative exclusion list for image captures.
func DecoyPaths() ([]string, error) {
	definitions, err := canonicalDecoys()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		paths = append(paths, definition.Path)
	}
	return paths, nil
}

func decoyMatches(format DecoyValueFormat, value string) bool {
	if len(value) != format.Length || !strings.HasPrefix(value, format.Prefix) {
		return false
	}
	for _, character := range []byte(value[len(format.Prefix):]) {
		if !strings.ContainsRune(format.Alphabet, rune(character)) {
			return false
		}
	}
	return true
}

// RenderDecoy renders one canonical template after checking every value.
func RenderDecoy(name string, values []string) (string, error) {
	definitions, err := canonicalDecoys()
	if err != nil {
		return "", err
	}
	for _, definition := range definitions {
		if definition.Name != name {
			continue
		}
		if len(values) != len(definition.Values) {
			return "", decoyFailure("the decoy generation is incomplete")
		}
		content := definition.Template
		for index, format := range definition.Values {
			if !decoyMatches(format, values[index]) {
				return "", decoyFailure("a decoy value differs from its fixed format")
			}
			content = strings.Replace(content, "{{"+format.Name+"}}", values[index], 1)
		}
		return content, nil
	}
	return "", decoyFailure("the decoy generation names an unknown file")
}

func validateDecoyContent(definition DecoyDefinition, content string) bool {
	template := definition.Template
	for _, format := range definition.Values {
		before, after, found := strings.Cut(template, "{{"+format.Name+"}}")
		if !found || !strings.HasPrefix(content, before) {
			return false
		}
		content = strings.TrimPrefix(content, before)
		if len(content) < format.Length || !decoyMatches(format, content[:format.Length]) {
			return false
		}
		content, template = content[format.Length:], after
	}
	return content == template
}

// ValidateDecoyContent checks one complete canonical file without exposing
// the parsed fake values or accepting a replacement template or path.
func ValidateDecoyContent(name, content string) error {
	definitions, err := canonicalDecoys()
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if definition.Name == name && validateDecoyContent(definition, content) {
			return nil
		}
	}
	return decoyFailure("the decoy file content is malformed")
}

// ValidateDecoyFilesV1 checks the complete generation before any home write.
// Nil is the disabled/none start. An explicit generation contains every row.
func ValidateDecoyFilesV1(input *DecoyFilesV1) error {
	if input == nil {
		return nil
	}
	definitions, err := canonicalDecoys()
	if err != nil {
		return err
	}
	if input.Version != 1 || len(input.Files) != len(definitions) {
		return decoyFailure("the decoy file generation is incomplete or unsupported")
	}
	seen := map[string]bool{}
	for _, file := range input.Files {
		index := slices.IndexFunc(definitions, func(definition DecoyDefinition) bool { return definition.Name == file.Name })
		if index < 0 || seen[file.Name] || !validateDecoyContent(definitions[index], file.Content) {
			return decoyFailure("the decoy file generation is malformed")
		}
		seen[file.Name] = true
	}
	return nil
}

package client

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

// envOption preserves the order of --env and --env-file in one command.
// Parsing has no command, host, grant or publication capability.
type envOption struct {
	value string
	file  bool
}

type envInputs struct {
	lookup   func(string) (string, bool)
	readFile func(context.Context, string) ([]byte, error)
	// configured contains ALL names from the canonical validated LOCAL
	// SecretDefinition view, not only requested or permitted secret names.
	configured map[string]bool
	// lookupFold is true only when the local lookup treats names without case,
	// as os.LookupEnv does on Windows. Guest keys remain case-sensitive.
	lookupFold bool
}

func envInputError(code, message, hint string, kind Kind) error {
	return &UserError{Code: code, Msg: message, Hint: hint, Kind: kind}
}

func checkEnvironmentName(name string, configured map[string]bool) error {
	// Reuse the existing canonical variable-name grammar. A Definition
	// migration must update this shared validator, never create a second one.
	if !secretNameRE.MatchString(name) {
		return envInputError("bad_env", "an environment entry has an invalid name", "Use a variable name containing letters, digits and underscores, starting with a letter or underscore.", KindUsage)
	}
	if name == "SSH_AUTH_SOCK" {
		return envInputError("env_is_secret", "SSH_AUTH_SOCK cannot be supplied as an ordinary variable", "Use the separately gated SSH agent option on an eligible unwalled session.", KindRefused)
	}
	if configured[name] {
		return envInputError("env_is_secret", name+" is a configured secret, so --env cannot pass it", "Pass this name with --secret so its value goes through the secret gate.", KindRefused)
	}
	return nil
}

func environmentEntry(entry string, inputs envInputs) (string, string, error) {
	name, value, literal := strings.Cut(entry, "=")
	if err := checkEnvironmentName(name, inputs.configured); err != nil {
		return "", "", err
	}
	if !literal {
		if inputs.lookupFold {
			for configured := range inputs.configured {
				if strings.EqualFold(name, configured) {
					return "", "", envInputError("env_is_secret", name+" aliases a configured laptop secret", "Pass the configured name with --secret.", KindRefused)
				}
			}
			if strings.EqualFold(name, "SSH_AUTH_SOCK") {
				return "", "", envInputError("env_is_secret", "this name aliases the laptop SSH agent socket", "Use the separately gated SSH agent option on an eligible unwalled session.", KindRefused)
			}
		}
		if inputs.lookup == nil {
			return "", "", envInputError("bad_env", "the laptop environment lookup is unavailable", "Read the variable on this computer before starting the session.", KindUsage)
		}
		var found bool
		value, found = inputs.lookup(name)
		if !found {
			return "", "", envInputError("env_missing", name+" is not set in the laptop environment", "Set this variable on this computer, or pass its literal value with --env NAME=VALUE.", KindUsage)
		}
	}
	if strings.ContainsRune(value, 0) {
		return "", "", envInputError("bad_env", "an environment value contains a NUL byte", "Use a value without NUL bytes.", KindUsage)
	}
	return name, value, nil
}

func environmentFile(data []byte) ([]string, error) {
	// The rooted reader bounds its input before returning it. The final
	// request also checks its exact encoded frame, including every option.
	if len(data) > proto.MaxFrame {
		return nil, envInputError("env_too_big", "the environment file exceeds the creation frame bound", "Use a smaller environment file.", KindUsage)
	}
	var entries []string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'}) // ordinary CRLF line ending
		if len(bytes.TrimSpace(line)) == 0 || bytes.HasPrefix(line, []byte{'#'}) {
			continue
		}
		entries = append(entries, string(line))
	}
	return entries, nil
}

func resolveStartEnvironment(ctx context.Context, layer map[string]string, options []envOption, inputs envInputs) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := maps.Clone(layer)
	if result == nil {
		result = map[string]string{}
	}
	for name, value := range layer {
		if err := checkEnvironmentName(name, inputs.configured); err != nil {
			return nil, err
		}
		if strings.ContainsRune(value, 0) {
			return nil, envInputError("bad_env", "a layer environment value contains a NUL byte", "Use a value without NUL bytes.", KindUsage)
		}
	}
	for _, option := range options {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries := []string{option.value}
		if option.file {
			if inputs.readFile == nil {
				return nil, envInputError("bad_env", "the environment file reader is unavailable", "Choose a readable local environment file.", KindUsage)
			}
			data, err := inputs.readFile(ctx, option.value)
			if err != nil {
				return nil, fmt.Errorf("read local environment file: %w", err)
			}
			entries, err = environmentFile(data)
			if err != nil {
				return nil, err
			}
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name, value, err := environmentEntry(entry, inputs)
			if err != nil {
				return nil, err
			}
			result[name] = value
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

package inputtransfer

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func decodeObject(data []byte, target any, allowed, required []string) (map[string]json.RawMessage, error) {
	fields, err := api.JSONObjectFields(data, allowed...)
	if err != nil {
		return nil, err
	}
	for name := range fields {
		if !slices.Contains(allowed, name) {
			return nil, protocol("unknown input metadata field")
		}
	}
	for _, name := range required {
		if len(fields[name]) == 0 || bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return nil, protocol("missing input metadata field")
		}
	}
	if err = json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	return fields, nil
}

func decodeEvent(data []byte, message *event) error {
	fields, err := decodeObject(data, message, []string{"event", "index", "nonce", "digest", "file"}, []string{"event", "index", "nonce", "digest"})
	if err != nil {
		return err
	}
	if message.File != nil {
		_, err = decodeObject(fields["file"], message.File, []string{"path", "size", "mode", "sha256", "directory", "include"}, []string{"path", "size", "mode", "sha256"})
	}
	if message.Event == "input.file" && message.File == nil || message.Event != "input.file" && len(fields["file"]) != 0 {
		return protocol("input event has an unexpected file declaration")
	}
	return err
}

func decodeItem(data []byte, declaration *item) error {
	names := []string{"kind", "index", "size", "sha256"}
	_, err := decodeObject(data, declaration, names, names)
	return err
}

func decodeEnd(data []byte, end *itemEnd) error {
	names := []string{"index", "sha256"}
	_, err := decodeObject(data, end, names, names)
	return err
}

func decodeSummary(data []byte, summary *api.InputSummary) error {
	fields, err := decodeObject(data, summary,
		[]string{"version", "nonce", "digest", "file_count", "copy_bytes", "repositories", "identity"},
		[]string{"version", "nonce", "digest", "file_count", "copy_bytes", "identity"})
	if err != nil {
		return err
	}
	if _, err = decodeObject(fields["identity"], &summary.Identity, []string{"known", "name", "email"}, []string{"known"}); err != nil {
		return err
	}
	if raw := fields["repositories"]; len(raw) > 0 {
		var entries []json.RawMessage
		if err = json.Unmarshal(raw, &entries); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return protocol("invalid input repository list")
		}
		for i, entry := range entries {
			if !api.ExactJSONFields(entry, api.InputRepository{}) {
				return protocol("input repository fields are not exact")
			}
			if _, err = decodeObject(entry, &summary.Repositories[i],
				[]string{"name", "source_branch", "commit", "origin", "ecosystems", "size", "sha256", "dirty", "object_format", "policy"},
				[]string{"name", "commit", "size", "sha256", "object_format"}); err != nil {
				return err
			}
		}
	}
	return nil
}

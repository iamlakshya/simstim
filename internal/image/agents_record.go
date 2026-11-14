package image

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// These records belong only to the host's canonical installed-agent producer.
// They use the SAME R10 domain and native-reference codecs as other image
// receipts. Neither record is client runtime or image pin authority.
type agentsRecordV1 struct {
	Version   int               `json:"version"`
	Domain    *cachefs.Domain   `json:"domain"`
	Store     cachefs.Reference `json:"store"`
	Directory cachefs.Reference `json:"directory"`
	Disk      AgentDiskV1       `json:"disk"`
	File      cachefs.Reference `json:"file"`
	Leaf      string            `json:"leaf"`
	Purge     *agentsPurgeV1    `json:"purge"`
}

type agentsPurgeV1 struct {
	Data     string `json:"data"`
	Metadata string `json:"metadata"`
}

type agentsSourceV1 struct {
	Version   int               `json:"version"`
	Domain    *cachefs.Domain   `json:"domain"`
	Store     cachefs.Reference `json:"store"`
	Directory cachefs.Reference `json:"directory"`
	Disk      AgentDiskV1       `json:"disk"`
}

func decodeAgentsRecord(raw []byte, domain cachefs.Domain, digest string) (agentsRecordV1, error) {
	var record agentsRecordV1
	fields, err := agentsFields(raw, "version", "domain", "store", "directory", "disk", "file", "leaf", "purge")
	if err != nil {
		return record, err
	}
	if _, err := agentsFields(fields["file"], "version", "identity", "mode", "size", "modified", "directory"); err != nil {
		return record, err
	}
	if err := agentsRootFields(fields); err != nil {
		return record, err
	}
	if !bytes.Equal(bytes.TrimSpace(fields["purge"]), []byte("null")) {
		if _, err := agentsFields(fields["purge"], "data", "metadata"); err != nil {
			return record, err
		}
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, agentsDiskError("the owned agents receipt cannot be decoded")
	}
	if err := domain.Validate(); err != nil {
		return record, err
	}
	if record.Version != 1 || record.Domain == nil || *record.Domain != domain || !agentsRootReference(record.Store) || !agentsRootReference(record.Directory) || !supply.IsHex64(digest) || record.Disk.Content.SHA256 != digest || !record.File.Known() || record.File.Directory || record.File.Mode != 0o444 || record.File.Size != record.Disk.Content.Size || record.Leaf != digest+".img" && !nonceLeaf(record.Leaf, ".agents-part-") {
		return record, agentsDiskError("the agents receipt has unknown, unbound or different ownership; existing files were preserved")
	}
	if record.Purge != nil && (!nonceLeaf(record.Purge.Data, ".agents-data-purge-") || !nonceLeaf(record.Purge.Metadata, ".agents-meta-purge-")) {
		return record, agentsDiskError("the agents removal intent has unknown capture names")
	}
	return record, nil
}

func decodeAgentsSource(raw []byte, domain cachefs.Domain) (agentsSourceV1, error) {
	var source agentsSourceV1
	fields, err := agentsFields(raw, "version", "domain", "store", "directory", "disk")
	if err != nil {
		return source, err
	}
	if err := agentsRootFields(fields); err != nil {
		return source, err
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return source, agentsDiskError("the newest agents source pointer cannot be decoded")
	}
	if err := domain.Validate(); err != nil {
		return source, err
	}
	if source.Version != 1 || source.Domain == nil || *source.Domain != domain || !agentsRootReference(source.Store) || !agentsRootReference(source.Directory) {
		return source, agentsDiskError("the newest agents pointer has unknown, unbound or different ownership; existing files were preserved")
	}
	return source, nil
}

func agentsRootReference(ref cachefs.Reference) bool {
	return ref.Known() && ref.Directory && ref.Mode == 0o700
}

func agentsRootFields(fields map[string]json.RawMessage) error {
	for _, name := range []string{"store", "directory"} {
		if _, err := agentsFields(fields[name], "version", "identity", "mode", "size", "modified", "directory"); err != nil {
			return err
		}
	}
	return nil
}

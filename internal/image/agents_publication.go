package image

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

func writeAgentsRecord(root *os.Root, record agentsRecordV1) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if record.Domain == nil {
		return agentsDiskError("an agents publication has no host domain")
	}
	if _, err := decodeAgentsRecord(raw, *record.Domain, record.Disk.Content.SHA256); err != nil {
		return err
	}
	return replaceReceipt(root, record.Disk.Content.SHA256+".json", raw)
}

// agentsRecordLeaf accepts only the exact creator-recorded inode at its
// initial part or final content name. A matching digest never adopts a file.
func agentsRecordLeaf(root *os.Root, record agentsRecordV1) (string, error) {
	final := record.Disk.Content.SHA256 + ".img"
	if record.Leaf == final {
		return final, nil
	}
	_, sourceErr := root.Lstat(record.Leaf)
	_, finalErr := root.Lstat(final)
	if sourceErr == nil && finalErr == nil {
		return "", agentsDiskError("both pending and final agents files remain; both were preserved")
	}
	if sourceErr == nil && errors.Is(finalErr, os.ErrNotExist) {
		return record.Leaf, nil
	}
	if errors.Is(sourceErr, os.ErrNotExist) && finalErr == nil {
		return final, nil
	}
	return "", agentsDiskError("the exact pending agents publication is missing or unsafe")
}

func checkAgentsRecordBytes(root *os.Root, record agentsRecordV1, expected supply.ContentRef) error {
	if record.Disk.Content != expected {
		return agentsDiskError("an existing producer receipt names different agents content")
	}
	leaf, err := agentsRecordLeaf(root, record)
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o444)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := record.File.CheckFile(root, leaf, file, 0o444); err != nil {
		return err
	}
	if err := supply.CheckContent(expected, file); err != nil {
		return err
	}
	if _, err := InspectAgentsV1(file); err != nil {
		return err
	}
	return record.File.CheckFile(root, leaf, file, 0o444)
}

// finishAgentsPublication runs only under the exact disk/source leases and
// placement gate. A persisted part witness makes each crash point replayable;
// no unreceipted final file, replacement inode or foreign collision is adopted.
func finishAgentsPublication(root *os.Root, record *agentsRecordV1) error {
	leaf, err := agentsRecordLeaf(root, *record)
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o444)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := record.File.CheckFile(root, leaf, file, 0o444); err != nil {
		return err
	}
	final := record.Disk.Content.SHA256 + ".img"
	if leaf != final {
		if err := cachefs.RenameNoReplace(root, leaf, final); err != nil {
			return err
		}
		if err := record.File.CheckFile(root, final, file, 0o444); err != nil {
			return err
		}
		if err := syncRoot(root); err != nil {
			return err
		}
	}
	if record.Leaf != final {
		record.Leaf = final
		return writeAgentsRecord(root, *record)
	}
	return nil
}

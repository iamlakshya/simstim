package supply

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

// The tools builder in guest/verity applies this same data limit. Keep
// verification bounded even when metadata describes a much larger input.
const maxToolsDataBytes = 512 << 20

// CheckToolsMetadata applies the same canonical geometry and build limit as
// the byte verifier, before a caller persists or fetches a tools item. It
// checks metadata only and creates no role or byte-provenance authority.
func CheckToolsMetadata(it Item) error {
	if it.Kind != KindTools {
		return errors.New("tools metadata needs the tools role")
	}
	return checkToolsBounds(it)
}

func checkToolsBounds(it Item) error {
	if err := it.validateTools(); err != nil {
		return err
	}
	if it.DataBlocks > maxToolsDataBytes/4096 {
		return errors.New("tools data exceeds the 512 MiB build limit")
	}
	return nil
}

func checkToolsTree(it Item, r io.ReadSeeker) error {
	if it.Kind != KindTools {
		return nil
	}
	if err := checkToolsBounds(it); err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek the tools image: %w", err)
	}
	dataBytes := it.DataBlocks * 4096
	tree, err := dmverity.MerkleTree(io.LimitReader(r, dataBytes))
	if err != nil {
		return fmt.Errorf("recompute the tools verity tree: %w", err)
	}
	if it.Size != dataBytes+int64(len(tree)) {
		return errors.New("tools size does not match its data blocks and verity tree")
	}
	gotRoot := hex.EncodeToString(dmverity.RootHash(tree))
	if gotRoot != it.RootHash {
		return errors.New("tools root_hash does not match its data blocks")
	}
	if _, err := r.Seek(dataBytes, io.SeekStart); err != nil {
		return fmt.Errorf("seek the stored tools verity tree: %w", err)
	}
	stored := make([]byte, len(tree))
	if _, err := io.ReadFull(r, stored); err != nil {
		return fmt.Errorf("read the stored tools verity tree: %w", err)
	}
	if !bytes.Equal(stored, tree) {
		return errors.New("tools verity tree does not match its data blocks")
	}
	return nil
}

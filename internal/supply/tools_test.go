package supply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

func verityTools(t *testing.T) (Item, []byte) {
	t.Helper()
	data := bytes.Repeat([]byte("x"), 8192)
	tree, err := dmverity.MerkleTree(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	all := slices.Concat(data, tree)
	item := NewItem(KindTools, "tools", "0.6.0", "amd64", all, base+ToolsAsset("amd64"))
	item.RootHash = hex.EncodeToString(dmverity.RootHash(tree))
	item.Salt = strings.Repeat("0", 64)
	item.DataBlocks = 2
	return item, all
}

func TestToolsCheckRecomputesTreeAndRoot(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Item, []byte)
	}{
		{"root hash", "root_hash", func(it *Item, _ []byte) { it.RootHash = hexA }},
		{"data with updated file digest", "root_hash", func(_ *Item, data []byte) { data[0] ^= 1 }},
		{"stored tree with updated file digest", "verity tree", func(_ *Item, data []byte) { data[8192] ^= 1 }},
		{"data block split", "size", func(it *Item, _ []byte) { it.DataBlocks = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, data := verityTools(t)
			tc.change(&item, data)
			// Keep SHA-256 coherent, so the old digest-only check cannot
			// accidentally detect the missing verity verification.
			sum := sha256.Sum256(data)
			item.SHA256 = hex.EncodeToString(sum[:])
			if err := Check(item, "tools", data); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Check = %v, want %s refusal", err, tc.want)
			}
			p := filepath.Join(t.TempDir(), ToolsAsset("amd64"))
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := CheckFile(item, p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckFile = %v, want %s refusal", err, tc.want)
			}
		})
	}
}

func TestToolsCheckAcceptsActualVerity(t *testing.T) {
	item, data := verityTools(t)
	if err := Check(item, "tools", data); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), ToolsAsset("amd64"))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckFile(item, p); err != nil {
		t.Fatal(err)
	}
}

func TestToolsCheckRejectsExtraTrailingBlocks(t *testing.T) {
	item, data := verityTools(t)
	data = append(data, make([]byte, 4096)...)
	sum := sha256.Sum256(data)
	item.Size, item.SHA256 = int64(len(data)), hex.EncodeToString(sum[:])
	if err := Check(item, "tools", data); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("extra trailing block = %v, want exact verity extent", err)
	}
}

func TestToolsCheckRefusesOversizedMetadataBeforeReading(t *testing.T) {
	item, _ := verityTools(t)
	item.DataBlocks = maxToolsDataBytes/4096 + 1
	item.Size = (item.DataBlocks + 1) * 4096
	r := &countingReader{}
	if err := checkReader(item, "tools", item.Size, r); err == nil || !strings.Contains(err.Error(), "512 MiB") {
		t.Fatalf("checkReader = %v, want bounded verification", err)
	}
	if r.reads != 0 {
		t.Fatalf("oversized metadata caused %d reads", r.reads)
	}
}

type countingReader struct{ reads int }

func (r *countingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

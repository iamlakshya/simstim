package supply

import (
	"bytes"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

// Exact canonical test fixture declarations extracted from tools_test.go and
// supply_test.go for a consumer without the rest of that test corpus.
var base = ReleaseFolder("0.6.0") + "/"

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

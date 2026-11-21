package supply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFSVerityDescriptorDigest(t *testing.T) {
	// Linux's documented empty-file descriptor has a zero Merkle root.
	const empty = "3d248ca542a24fc62d1c43b916eae5016878e2533c88238480b26128a1f1af95"
	got, err := FSVerityDigest(context.Background(), bytes.NewReader(nil), 0)
	if err != nil || got != empty {
		t.Fatal(got, err)
	}
	for _, size := range []int{1, 4095, 4096, 4097, 128 * 4096, 128*4096 + 1} {
		data := bytes.Repeat([]byte{0x25}, size)
		got, err := FSVerityDigest(context.Background(), bytes.NewReader(data), int64(size))
		if err != nil || got != flatVerityReference(data) {
			t.Fatalf("size %d: %s %v", size, got, err)
		}
	}
	// Padding is shared, but the descriptor must bind the exact file size.
	a, _ := FSVerityDigest(context.Background(), bytes.NewReader([]byte{1}), 1)
	b, _ := FSVerityDigest(context.Background(), bytes.NewReader([]byte{1, 0}), 2)
	if a == b {
		t.Fatal("file size was omitted from the descriptor digest")
	}
}

// This test oracle materialises levels, unlike the production bounded walk.
func flatVerityReference(data []byte) string {
	var root [32]byte
	var hashes []byte
	for offset := 0; offset < len(data); offset += 4096 {
		var block [4096]byte
		copy(block[:], data[offset:min(offset+4096, len(data))])
		h := sha256.Sum256(block[:])
		hashes = append(hashes, h[:]...)
	}
	for len(hashes) > 32 {
		var next []byte
		for offset := 0; offset < len(hashes); offset += 4096 {
			var block [4096]byte
			copy(block[:], hashes[offset:min(offset+4096, len(hashes))])
			h := sha256.Sum256(block[:])
			next = append(next, h[:]...)
		}
		hashes = next
	}
	copy(root[:], hashes)
	var descriptor [256]byte
	descriptor[0], descriptor[1], descriptor[2] = 1, 1, 12
	binary.LittleEndian.PutUint64(descriptor[8:], uint64(len(data)))
	copy(descriptor[16:], root[:])
	h := sha256.Sum256(descriptor[:])
	return hex.EncodeToString(h[:])
}

func TestFSVerityDigestRefusesShortReadAndCancellation(t *testing.T) {
	if _, err := FSVerityDigest(context.Background(), bytes.NewReader([]byte{1}), 2); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FSVerityDigest(ctx, bytes.NewReader(nil), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := FSVerityDigest(context.Background(), bytes.NewReader(nil), -1); err == nil {
		t.Fatal("accepted negative file size")
	}
}

func TestFSVerityManifestBindingAndDiskExclusion(t *testing.T) {
	it := NewItem(KindKernel, "guest", "6.18", "amd64", []byte("fixed kernel bytes"), base+KernelAsset("amd64"))
	if !IsHex64(it.FSVerity) {
		t.Fatal("actual runtime bytes did not produce fsverity")
	}
	it.FSVerity = strings.Repeat("a", 64)
	var mismatch *MismatchError
	if err := Check(it, it.Name, []byte("fixed kernel bytes")); !errors.As(err, &mismatch) || mismatch.What != "fs-verity" {
		t.Fatal(err)
	}
	for _, kind := range []string{KindTools, KindImage, KindVMImage, KindGvisor, KindHelper} {
		if err := (Item{Kind: kind, FSVerity: strings.Repeat("a", 64)}).validateFSVerity(); err == nil {
			t.Fatal("accepted fsverity for", kind)
		}
	}
	if err := (Item{Kind: KindVMM, FSVerity: "SHA256:wrong"}).validateFSVerity(); err == nil {
		t.Fatal("accepted noncanonical fsverity")
	}
}

func TestFSVerityDigestAgainstUtility(t *testing.T) {
	tool := os.Getenv("FSVERITY_REFERENCE")
	if tool == "" {
		t.Skip("FSVERITY_REFERENCE is not set to an admitted fsverity utility; no external utility proof claimed")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("reference utility must be an absolute admitted path")
	}
	// This command computes digests only. It never enables verity or needs root.
	for _, data := range [][]byte{nil, []byte("fixed fs-verity reference\n"), bytes.Repeat([]byte{0x5a}, 129*4096+3)} {
		p := filepath.Join(t.TempDir(), "fixture")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, tool, "digest", "--hash-alg=sha256", "--block-size=4096", p)
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		want, err := FSVerityDigest(context.Background(), bytes.NewReader(data), int64(len(data)))
		fields := strings.Fields(string(out))
		if err != nil || len(fields) != 2 || fields[0] != "sha256:"+want || fields[1] != p {
			t.Fatalf("reference mismatch: %q %v", out, err)
		}
	}
}

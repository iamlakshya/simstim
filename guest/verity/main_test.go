package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendTreePadsAndMatchesManualMerkle(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "tools-amd64.erofs")
	meta := filepath.Join(dir, "tools-amd64.verity.json")

	// 5000 bytes pads to 2 blocks (8192 bytes), followed by 1 hash block (4096 bytes)
	// with no verity superblock in between.
	raw := bytes.Repeat([]byte("abcd1234"), 625)
	if err := os.WriteFile(img, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"-meta", meta, img}, &stdout); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	var got Result
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v", err)
	}
	metaBytes, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(metaBytes, stdout.Bytes()) {
		t.Fatalf("meta file differs from stdout")
	}

	if got.DataBlocks != 2 {
		t.Fatalf("data_blocks = %d, want 2", got.DataBlocks)
	}
	if got.Salt != ZeroSalt || len(got.Salt) != 64 {
		t.Fatalf("salt = %q, want 64 zero hex digits", got.Salt)
	}
	if got.Size != 3*BlockSize {
		t.Fatalf("size = %d, want %d (no superblock)", got.Size, 3*BlockSize)
	}

	full, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(full)) != got.Size {
		t.Fatalf("file len = %d, want %d", len(full), got.Size)
	}
	sum := sha256.Sum256(full)
	if hex.EncodeToString(sum[:]) != got.SHA256 {
		t.Fatalf("sha256 = %s, want %x", got.SHA256, sum)
	}
	// Bytes 5000..8192 must be zero padding; bytes 8192..12288 must NOT start with "verity\0\0".
	for i := 5000; i < 2*BlockSize; i++ {
		if full[i] != 0 {
			t.Fatalf("non-zero pad byte at %d: %d", i, full[i])
		}
	}
	if bytes.HasPrefix(full[2*BlockSize:], []byte("verity\x00\x00")) {
		t.Fatal("unexpected dm-verity superblock at start of hash tree")
	}

	// Manually compute the Linux dm-verity SHA-256 root hash for 2 blocks with 32 zero-byte salt.
	zeroSalt := make([]byte, 32)
	saltedHash := func(block []byte) []byte {
		h := sha256.New()
		h.Write(zeroSalt)
		h.Write(block)
		return h.Sum(nil)
	}
	h0 := saltedHash(full[0:BlockSize])
	h1 := saltedHash(full[BlockSize : 2*BlockSize])
	hashBlock := make([]byte, BlockSize)
	copy(hashBlock[0:32], h0)
	copy(hashBlock[32:64], h1)
	if !bytes.Equal(full[2*BlockSize:3*BlockSize], hashBlock) {
		t.Fatal("appended hash block does not match expected salted block hashes")
	}
	wantRoot := hex.EncodeToString(saltedHash(hashBlock))
	if got.RootHash != wantRoot {
		t.Fatalf("root_hash = %s, want %s", got.RootHash, wantRoot)
	}
}

func TestAppendTreeDeterministicAndDetectsChange(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.erofs")
	b := filepath.Join(dir, "b.erofs")
	c := filepath.Join(dir, "c.erofs")

	// Multi-level tree: 200 blocks (exceeds 128 hashes per 4096-byte block).
	data := bytes.Repeat([]byte{0x5a, 0xa5, 0x11, 0x22}, (200*BlockSize)/4)
	for _, p := range []string{a, b, c} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Flip 1 bit in c before appending the tree.
	cData := append([]byte(nil), data...)
	cData[12345] ^= 0x01
	if err := os.WriteFile(c, cData, 0o644); err != nil {
		t.Fatal(err)
	}

	ra, err := AppendTree(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := AppendTree(b)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := AppendTree(c)
	if err != nil {
		t.Fatal(err)
	}
	if ra != rb {
		t.Fatalf("two runs differed: %+v vs %+v", ra, rb)
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if !bytes.Equal(ba, bb) {
		t.Fatal("output bytes differed across identical inputs")
	}
	if ra.RootHash == rc.RootHash || ra.SHA256 == rc.SHA256 {
		t.Fatalf("1-bit change produced same root_hash or sha256: %+v vs %+v", ra, rc)
	}
}

func TestAppendTreeRejectsBadInputsWithTryHint(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.erofs")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real.erofs")
	if err := os.WriteFile(target, make([]byte, BlockSize*2), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.erofs")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"bad flag", []string{"-nope"}},
		{"missing file", []string{filepath.Join(dir, "missing.erofs")}},
		{"empty file", []string{empty}},
		{"directory", []string{dir}},
		{"symlink", []string{link}},
		{"unwritable meta", []string{"-meta", filepath.Join(dir, "no-such-dir", "m.json"), target}},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		err := run(tc.args, &out)
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		var f *failure
		if !errors.As(err, &f) || strings.TrimSpace(f.try) == "" {
			t.Errorf("%s: error %q has no Try hint", tc.name, err)
		}
	}
}

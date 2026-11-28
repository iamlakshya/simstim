package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestVerityCLIRoundTripAndTamperReject(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "tools-amd64.erofs")
	meta := img + ".json"
	if err := os.WriteFile(img, bytes.Repeat([]byte("X"), 5000), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"-meta", meta, img}); err != nil {
		t.Fatalf("verity append: %v", err)
	}
	raw, err := os.ReadFile(meta)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var info supply.VerityInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if info.DataBlocks != 2 || info.Salt != supply.ZeroSaltHex {
		t.Fatalf("unexpected info: %+v", info)
	}

	if err := run([]string{
		"-verify",
		"-root-hash", info.RootHash,
		"-data-blocks", strconv.FormatUint(info.DataBlocks, 10),
		img,
	}); err != nil {
		t.Fatalf("verity -verify: %v", err)
	}

	// Flip 1 byte in data and confirm -verify rejects it.
	b, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	b[42] ^= 0x80
	if err := os.WriteFile(img, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"-verify",
		"-root-hash", info.RootHash,
		"-data-blocks", strconv.FormatUint(info.DataBlocks, 10),
		img,
	}); err == nil {
		t.Fatal("expected -verify to fail after tampering")
	}
}

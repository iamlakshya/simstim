// Command verity pads a tools image to a 4096-byte multiple and appends (or
// verifies) a raw SHA-256 dm-verity Merkle tree with no verity superblock and
// a 32-zero-byte salt (isolation spec 20.1).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("verity", flag.ContinueOnError)
	verify := fs.Bool("verify", false, "verify an existing raw dm-verity tree instead of appending one")
	rootHash := fs.String("root-hash", "", "expected SHA-256 root hash (with -verify)")
	dataBlocks := fs.Uint64("data-blocks", 0, "number of 4096-byte data blocks (with -verify)")
	metaPath := fs.String("meta", "", "write JSON metadata to this file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("verity: %w. Try: go run ./guest/verity [-meta file.json] <image>", err)
	}
	if fs.NArg() != 1 {
		return errors.New("verity: expected one image path. Try: go run ./guest/verity <tools.erofs>")
	}
	imgPath := fs.Arg(0)

	var info supply.VerityInfo
	var err error
	if *verify {
		info, err = supply.VerifyRawVerity(imgPath, *dataBlocks, *rootHash)
	} else {
		info, err = supply.AppendRawVerity(imgPath)
	}
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("verity: encode metadata: %w", err)
	}
	raw = append(raw, '\n')
	if *metaPath != "" {
		if err := os.WriteFile(*metaPath, raw, 0o600); err != nil {
			return fmt.Errorf("verity: write %s: %w", *metaPath, err)
		}
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		return fmt.Errorf("verity: write stdout: %w", err)
	}
	return nil
}

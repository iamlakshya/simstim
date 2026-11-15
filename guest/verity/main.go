// Command verity pads an image file with zeros to a 4096-byte block boundary
// and appends a dm-verity Merkle tree with no verity superblock (isolation
// spec 20.1). It uses MerkleTree and RootHash from hcsshim's dmverity package:
// SHA-256, 4096-byte blocks, and a salt of 32 zero bytes.
//
//	go run ./guest/verity [-meta tools-amd64.verity.json] tools-amd64.erofs
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

const (
	// BlockSize is the dm-verity data and hash block size in bytes.
	BlockSize = 4096
	// ZeroSalt is 32 zero bytes in lower-case hex, matching dmverity's salt.
	ZeroSalt = "0000000000000000000000000000000000000000000000000000000000000000"
	// maxImageBytes caps the input image so a bad file cannot exhaust memory.
	maxImageBytes = 512 << 20
)

// Result holds the whole file's digest and size, and its dm-verity table parameters.
type Result struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	RootHash   string `json:"root_hash"`
	Salt       string `json:"salt"`
	DataBlocks int64  `json:"data_blocks"`
}

type failure struct {
	msg, try string
}

func (f *failure) Error() string { return f.msg }

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "verity:", err)
		var f *failure
		if errors.As(err, &f) && f.try != "" {
			fmt.Fprintln(os.Stderr, "Try:", f.try)
		}
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	metaPath := fs.String("meta", "", "write the JSON result to this file as well")
	if err := fs.Parse(args); err != nil {
		return &failure{msg: err.Error(), try: "go run ./guest/verity [-meta out.json] <image.erofs>"}
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return &failure{msg: "needs one image file", try: "go run ./guest/verity [-meta out.json] <image.erofs>"}
	}
	res, err := AppendTree(fs.Arg(0))
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return &failure{msg: "marshal verity metadata: " + err.Error(), try: "run the build again"}
	}
	b = append(b, '\n')
	if *metaPath != "" {
		if err := os.WriteFile(*metaPath, b, 0o644); err != nil { //nolint:gosec // build metadata holds public digests, not secrets
			return &failure{msg: "write " + *metaPath + ": " + err.Error(), try: "check that the output folder exists and can be written"}
		}
	}
	if _, err := out.Write(b); err != nil {
		return &failure{msg: "write output: " + err.Error(), try: "run the build again"}
	}
	return nil
}

// AppendTree pads path with zeros to a multiple of 4096 bytes, appends its
// dm-verity Merkle tree with no superblock, and returns the resulting digests
// and block count.
func AppendTree(path string) (Result, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return Result{}, &failure{msg: "open image folder: " + err.Error(), try: "build the EROFS image with mkfs.erofs first"}
	}
	defer root.Close()
	name := filepath.Base(path)
	fi, err := root.Lstat(name)
	if err != nil {
		return Result{}, &failure{msg: "stat " + path + ": " + err.Error(), try: "build the EROFS image with mkfs.erofs first"}
	}
	if !fi.Mode().IsRegular() {
		return Result{}, &failure{msg: path + " is not a regular file", try: "pass the regular image file that mkfs.erofs wrote"}
	}
	if fi.Size() <= 0 {
		return Result{}, &failure{msg: path + " is empty", try: "build a non-empty EROFS image with mkfs.erofs first"}
	}
	if fi.Size() > maxImageBytes {
		return Result{}, &failure{msg: fmt.Sprintf("%s is %d bytes, over the %d-byte cap", path, fi.Size(), maxImageBytes),
			try: "keep only /init, the shadow terminal and the mount folders in the tools image"}
	}

	f, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return Result{}, &failure{msg: "open " + path + ": " + err.Error(), try: "check that the image file is writable"}
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(fi, opened) || fi.Size() != opened.Size() {
		return Result{}, &failure{msg: path + " changed while opening the image", try: "do not modify the image while the build runs"}
	}

	size := fi.Size()
	if rem := size % BlockSize; rem != 0 {
		pad := make([]byte, BlockSize-rem)
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return Result{}, &failure{msg: "seek " + path + ": " + err.Error(), try: "run the build again on a local filesystem"}
		}
		if _, err := f.Write(pad); err != nil {
			return Result{}, &failure{msg: "pad " + path + ": " + err.Error(), try: "check free disk space and run the build again"}
		}
		size += int64(len(pad))
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{}, &failure{msg: "seek " + path + ": " + err.Error(), try: "run the build again"}
	}
	tree, err := dmverity.MerkleTree(io.LimitReader(f, size))
	if err != nil {
		return Result{}, &failure{msg: "build verity tree for " + path + ": " + err.Error(), try: "check the image file and run the build again"}
	}
	if len(tree) < BlockSize || len(tree)%BlockSize != 0 {
		return Result{}, &failure{msg: fmt.Sprintf("verity tree for %s has invalid length %d", path, len(tree)), try: "check the dmverity version in go.mod"}
	}
	rootHash := hex.EncodeToString(dmverity.RootHash(tree))

	if _, err := f.Seek(size, io.SeekStart); err != nil {
		return Result{}, &failure{msg: "seek " + path + ": " + err.Error(), try: "run the build again"}
	}
	if _, err := f.Write(tree); err != nil {
		return Result{}, &failure{msg: "append verity tree to " + path + ": " + err.Error(), try: "check free disk space and run the build again"}
	}

	total := size + int64(len(tree))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{}, &failure{msg: "seek " + path + ": " + err.Error(), try: "run the build again"}
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, total+1))
	if err != nil || n != total {
		return Result{}, &failure{msg: "hash " + path + " after appending verity tree", try: "do not modify the image while the build runs"}
	}
	if err := f.Close(); err != nil {
		return Result{}, &failure{msg: "close " + path + ": " + err.Error(), try: "check free disk space and run the build again"}
	}

	return Result{
		SHA256:     hex.EncodeToString(h.Sum(nil)),
		Size:       total,
		RootHash:   rootHash,
		Salt:       ZeroSalt,
		DataBlocks: size / BlockSize,
	}, nil
}

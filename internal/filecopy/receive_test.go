package filecopy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCopyCapUsesKnownPolicyAndRejectsInvalidExplicitSizes(t *testing.T) {
	if _, err := CopyMax(nil); err == nil {
		t.Fatal("unknown policy became a default ceiling")
	}
	policy := api.Policy{}
	if limit, err := CopyMax(&policy); err != nil || limit != 256<<20 {
		t.Fatalf("known default = %d %v", limit, err)
	}
	for _, value := range []string{"", "0M", "-1M", "999999999999T"} {
		policy.Sandbox.CPMax = &value
		if _, err := CopyMax(&policy); err == nil {
			t.Fatalf("invalid explicit cap %q admitted", value)
		}
	}
}

func TestReceiveFileExactBytesDigestAndNoTrailingPayload(t *testing.T) {
	data := []byte("ordinary file bytes")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name    string
		payload []byte
		hash    string
	}{{"short", data[:len(data)-1], digest}, {"extra", append(append([]byte(nil), data...), 0), digest}, {"digest", data, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := batchRoot(t)
			if err := ReceiveFile(t.Context(), root, "notes", int64(len(data)), test.hash, false, bytes.NewReader(test.payload)); err == nil {
				t.Fatal("malformed transfer published")
			}
			if _, err := root.Lstat("notes"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed transfer published final name")
			}
			file, err := root.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := file.ReadDir(-1)
			file.Close()
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed transfer left an owned temp: %v %v", entries, err)
			}
		})
	}
}

func TestGuestCopyPathsAndPortableNames(t *testing.T) {
	top, relative, err := GuestPath("~/notes.md")
	if err != nil || top != "/home/agent" || relative != "notes.md" {
		t.Fatalf("guest home path = %q %q %v", top, relative, err)
	}
	for _, path := range []string{"/etc/shadow", "/work/a/../b", "/work/.git/config", "/tmp/\x1bname", "relative", "/work//bad"} {
		if _, _, err := GuestPath(path); err == nil {
			t.Fatalf("unsafe guest path %q accepted", path)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "\x00", ".git"} {
		if err := Name(name); err == nil {
			t.Fatalf("unsafe component %q accepted", name)
		}
	}
}

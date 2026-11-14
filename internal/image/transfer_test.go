package image

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCopiedLayersAreRecheckedAndCleaned(t *testing.T) {
	store := imageStore(t)
	_, request, documents, layers, objects := imageRegistryFixture(t)
	transfer, err := store.Begin(request)
	if err != nil {
		t.Fatal(err)
	}
	token := transfer.Progress().Token
	for _, document := range documents {
		if err := transfer.WriteAt(context.Background(), document.Digest, 0, document.Data); err != nil {
			t.Fatal(err)
		}
	}
	body := append([]byte(nil), objects[layers[0].Digest]...)
	body[len(body)-1] ^= 1
	if err := transfer.WriteAt(context.Background(), layers[0].Digest, 0, body); err != nil {
		t.Fatal(err)
	}
	_, err = transfer.Commit(context.Background(), testConvert)
	assertFlatCode(t, err, "digest_mismatch")
	if _, err := store.root.Lstat("incoming/" + token); !os.IsNotExist(err) {
		t.Fatalf("bad copied bytes remain: %v", err)
	}
}

func TestTransferRejectsPathsOverlapAndOversizedChunks(t *testing.T) {
	store := imageStore(t)
	_, request, _, layers, _ := imageRegistryFixture(t)
	transfer, err := store.Begin(request)
	if err != nil {
		t.Fatal(err)
	}
	defer transfer.Close()
	for _, args := range []struct {
		digest string
		offset int64
		data   []byte
	}{{"../../elsewhere", 0, []byte("x")}, {layers[0].Digest, -1, []byte("x")}, {layers[0].Digest, 1, []byte("x")}, {layers[0].Digest, 0, make([]byte, MaxChunk+1)}} {
		if err := transfer.WriteAt(context.Background(), args.digest, args.offset, args.data); err == nil {
			t.Fatal("invalid transfer chunk accepted")
		}
	}
	if err := transfer.WriteAt(context.Background(), layers[0].Digest, 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := transfer.WriteAt(context.Background(), layers[0].Digest, 0, []byte("b")); err == nil {
		t.Fatal("overlapping chunk accepted")
	}
}

func TestCopiedManifestCannotChooseAnotherDigest(t *testing.T) {
	_, request, documents, _, _ := imageRegistryFixture(t)
	documents[0].Data = append(documents[0].Data, ' ')
	_, err := CheckDocuments(request, map[string][]byte{request.Digest: documents[0].Data})
	assertFlatCode(t, err, "digest_mismatch")
}

func TestImageRecordCannotMoveClientPins(t *testing.T) {
	_, request, _, _, _ := imageRegistryFixture(t)
	record := Record{Ref: request.Ref, Digest: request.Digest, PlatformDigest: request.PlatformDigest, Arch: request.Arch, RootHash: strings.Repeat("a", 64), DataBlocks: 1, Size: 12288, ConvertedAt: time.Now()}
	if err := CheckRecord(request, record); err != nil {
		t.Fatal(err)
	}
	record.Digest = "sha256:" + strings.Repeat("b", 64)
	assertFlatCode(t, CheckRecord(request, record), "image_protocol")
	record.Digest = request.Digest
	request.RootHash = strings.Repeat("c", 64)
	request.DataBlocks = 1
	assertFlatCode(t, CheckRecord(request, record), "image_root_mismatch")
}

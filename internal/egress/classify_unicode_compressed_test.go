package egress

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func classifierTestGzip(t *testing.T, raw string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestClassifierCompressedAssetPreservesWholeRawCorpusAndBothPins(t *testing.T) {
	raw, err := decodeClassifierConfusables(classifierConfusableCompressed)
	if err != nil || len(raw) != classifierConfusableRawBytes {
		t.Fatal("pinned representation did not decode the exact original size", err)
	}
	digest := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(digest[:]) != classifierConfusableDataSHA256 {
		t.Fatal("compressed representation changed an original corpus byte")
	}
	prototypes, err := parseClassifierConfusables(raw)
	if err != nil || len(prototypes) != 6311 {
		t.Fatal("same pinned prototype parser lost part of the full corpus", err)
	}
	changedHeader := append([]byte(nil), classifierConfusableCompressed...)
	changedHeader[4] = 1 // source data stays exact; encoded whole asset differs
	for _, changed := range [][]byte{
		append(append([]byte(nil), classifierConfusableCompressed...), 0),
		append(append([]byte(nil), classifierConfusableCompressed...), classifierConfusableCompressed...),
		changedHeader,
	} {
		if _, err := decodeClassifierConfusables(changed); err == nil {
			t.Fatal("edited encoded bytes retained their whole-asset identity")
		}
	}
}

func TestClassifierCompressedPayloadRejectsMembersTailsSizesAndRawEdits(t *testing.T) {
	raw, err := decodeClassifierConfusables(classifierConfusableCompressed)
	if err != nil {
		t.Fatal(err)
	}
	changedRaw := []byte(raw)
	changedRaw[len(changedRaw)/2] ^= 1
	for _, changed := range [][]byte{
		append(append([]byte(nil), classifierConfusableCompressed...), 0),
		append(append([]byte(nil), classifierConfusableCompressed...), classifierConfusableCompressed...),
		classifierConfusableCompressed[:len(classifierConfusableCompressed)-1],
		classifierTestGzip(t, raw+"x"),
		classifierTestGzip(t, raw[:len(raw)-1]),
		classifierTestGzip(t, string(changedRaw)),
	} {
		if _, err := decodeClassifierConfusablePayload(changed); err == nil {
			t.Fatal("actual bounded decode accepted extra members/tail, wrong size or changed raw identity")
		}
	}
}

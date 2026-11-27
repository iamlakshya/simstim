package vmneed

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
)

func TestImageProjectionPreservesOriginalPinsAndOwnsSavedIdentity(t *testing.T) {
	original := fixtureNeed().Image
	raw, err := EncodeImageV1(original)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := DecodeImageV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeImageV1(actual)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("original image pins changed")
	}
	saved := original.Clone()
	saved.Ref = "toolchain"
	saved.PlatformDigest = saved.Digest
	saved.SavedHost = &image.SavedHostV1{Version: 1, HostID: strings.Repeat("f", 32), Ref: saved.Ref, Digest: saved.Digest}
	encoded, err := EncodeImageV1(saved)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeImageV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded.SavedHost.Ref = "other"
	if saved.SavedHost.Ref != "toolchain" {
		t.Fatal("decoded identity aliases original source")
	}
}
func TestImageProjectionRejectsAliasesDuplicatesTrailingAndTransfer(t *testing.T) {
	original := fixtureNeed().Image
	raw, err := EncodeImageV1(original)
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]byte{
		nil, []byte("null"), append(bytes.Clone(raw), []byte(" {}")...),
		bytes.Replace(raw, []byte(`"ref"`), []byte(`"Ref"`), 1),
		append([]byte(`{"ref":"other",`), raw[1:]...),
		[]byte(strings.Repeat(" ", MaxImageV1+1)),
	}
	for _, input := range cases {
		if _, err := DecodeImageV1(input); err == nil {
			t.Fatal("invalid image projection accepted")
		}
	}
	original.Transfer = strings.Repeat("1", 32)
	if _, err := EncodeImageV1(original); err == nil {
		t.Fatal("ephemeral transfer entered original image history")
	}
	wire, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeImageV1(wire); err == nil {
		t.Fatal("wire transfer entered original image compare")
	}
}

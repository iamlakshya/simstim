package image

import (
	"encoding/json"
	"strings"
	"testing"
)

func savedIdentityRequest() Request {
	digest := "sha256:" + strings.Repeat("a", 64)
	return Request{Ref: "a..", Digest: digest, PlatformDigest: digest, Arch: "amd64", RootHash: strings.Repeat("b", 64), DataBlocks: 1, SavedHost: &SavedHostV1{Version: 1, HostID: strings.Repeat("c", 32), Ref: "a..", Digest: digest}}
}

func savedIdentityRecord(request Request) Record {
	return Record{Ref: request.Ref, Digest: request.Digest, PlatformDigest: request.PlatformDigest, Arch: request.Arch, RootHash: request.RootHash, DataBlocks: request.DataBlocks, Size: convertedBound(4096), SavedHost: CloneSavedHostV1(request.SavedHost), SavedAliases: []string{request.Ref, "tools"}}
}

func TestSavedRequestKeepsLocalAliasAndCannotBecomeOCIOrTransferAuthority(t *testing.T) {
	request := savedIdentityRequest()
	if err := request.Validate(); err != nil {
		t.Fatal("the host-local name was forced through OCI grammar", err)
	}
	for _, change := range []func(*Request){
		func(request *Request) { request.SavedHost = nil },
		func(request *Request) { request.SavedHost.Version++ },
		func(request *Request) { request.SavedHost.HostID = "elsewhere" },
		func(request *Request) { request.SavedHost.Ref = "other" },
		func(request *Request) { request.SavedHost.Digest = "sha256:" + strings.Repeat("d", 64) },
		func(request *Request) { request.PlatformDigest = "sha256:" + strings.Repeat("d", 64) },
		func(request *Request) { request.RootHash = ""; request.DataBlocks = 0 },
		func(request *Request) { request.Transfer = strings.Repeat("d", 32) },
	} {
		changed := request.Clone()
		change(&changed)
		if changed.Validate() == nil {
			t.Fatal("a mismatched local saved pin became image authority")
		}
	}
	clone := request.Clone()
	clone.SavedHost.Ref = "changed"
	if request.SavedHost.Ref != "a.." {
		t.Fatal("a derived request borrowed the original host identity")
	}
}

func TestSavedAliasProjectionPreservesOriginalRootMetadata(t *testing.T) {
	first := savedIdentityRequest()
	original := savedIdentityRecord(first)
	second := first.Clone()
	second.Ref, second.SavedHost.Ref = "tools", "tools"
	projected, err := projectSavedSelection(second, original)
	if err != nil || projected.Ref != "tools" || projected.SavedHost.Ref != "tools" {
		t.Fatal("another checked alias could not select the same native root", err)
	}
	if original.Ref != "a.." || original.SavedHost.Ref != "a.." {
		t.Fatal("alias selection rewrote the first producer's root identity")
	}
	projected.SavedAliases[0] = "caller mutation"
	if original.SavedAliases[0] != "a.." {
		t.Fatal("an alias projection borrowed the metadata vector")
	}
	second.Ref, second.SavedHost.Ref = "unknown", "unknown"
	if _, err := projectSavedSelection(second, original); err == nil {
		t.Fatal("public metadata invented a missing alias")
	}
	oci := first.Clone()
	oci.SavedHost = nil
	if checkSavedSelection(oci, original) == nil {
		t.Fatal("a saved root was relabelled as an OCI manifest")
	}
	if err := CheckRecord(first, original); err != nil {
		t.Fatal(err)
	}
}

func TestSavedHostIdentityDecoderRefusesFutureAliasesAndDuplicateFields(t *testing.T) {
	request := savedIdentityRequest()
	raw, err := json.Marshal(request.SavedHost)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.Replace(string(raw), `"v":1`, `"v":2`, 1),
		strings.Replace(string(raw), `"host_id":`, `"Host_ID":`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"v":1`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"path":"/foreign"`, 1),
	} {
		var identity SavedHostV1
		if json.Unmarshal([]byte(body), &identity) == nil {
			t.Fatal("an unsupported identity form entered the saved root path")
		}
	}
}

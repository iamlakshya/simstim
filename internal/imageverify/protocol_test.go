package imageverify

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func protocolRequest() RequestV1 {
	return RequestV1{Version: Version, Role: Role, Nonce: strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("b", 64), Identity: "release@example.com", Issuer: "https://issuer.example", TrustedRoot: []byte("root"), Bundle: []byte("bundle")}
}

func TestVerifierProtocolBindsCompleteOriginalRequest(t *testing.T) {
	request, _ := json.Marshal(protocolRequest())
	parsed, wire, err := ReadRequest(bytes.NewReader(request))
	if err != nil || parsed.Nonce != protocolRequest().Nonce || !bytes.Equal(wire, request) {
		t.Fatalf("request: %v", err)
	}
	response, _ := json.Marshal(ResponseV1{Version: Version, Role: Role, HelperVersion: "1.2.3", RequestSHA256: RequestDigest(request)})
	if err := CheckResponse(response, "1.2.3", request); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"nonce", "digest", "identity", "issuer", "trusted_root", "bundle", "payload"} {
		t.Run(field, func(t *testing.T) {
			var changed map[string]any
			json.Unmarshal(request, &changed)
			changed[field] = "changed"
			other, _ := json.Marshal(changed)
			if CheckResponse(response, "1.2.3", other) == nil {
				t.Fatal("result accepted another request")
			}
		})
	}
	if CheckResponse(response, "other", request) == nil {
		t.Fatal("result accepted another executable version")
	}
}

func TestVerifierProtocolRejectsAliasesDuplicatesTrailingAndOversize(t *testing.T) {
	request, _ := json.Marshal(protocolRequest())
	for _, data := range [][]byte{
		append(bytes.Clone(request), []byte(" {}")...),
		bytes.Replace(request, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(request, []byte(`"version":1`), []byte(`"Version":1`), 1),
		bytes.Repeat([]byte(" "), MaxRequest+1),
	} {
		if _, _, err := ReadRequest(bytes.NewReader(data)); err == nil {
			t.Fatal("invalid request was accepted")
		}
	}
	response, _ := json.Marshal(ResponseV1{Version: Version, Role: Role, HelperVersion: "1", RequestSHA256: RequestDigest(request)})
	if CheckResponse(append(response, []byte(" {}")...), "1", request) == nil {
		t.Fatal("trailing response accepted")
	}
}

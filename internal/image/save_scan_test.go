package image

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSavedRootScannerFindsRawAndEveryEmbeddedBase64ByteAlignmentAcrossReads(t *testing.T) {
	secret := api.Secret("original-secret-value-0123456789")
	for _, text := range []string{string(secret), "prefix:" + string(secret) + ":suffix", base64.StdEncoding.EncodeToString([]byte(string(secret))), base64.StdEncoding.EncodeToString([]byte("x" + string(secret) + "tail")), base64.StdEncoding.EncodeToString([]byte("xy" + string(secret) + "tail"))} {
		for _, chunk := range []int{1, 2, 7, 32 << 10} {
			scanner, err := newSaveScanner(map[string]api.Secret{"TOKEN": secret})
			if err != nil {
				t.Fatal(err)
			}
			scanner.reset("/etc/tool.conf")
			var refusal error
			for offset := 0; offset < len(text); offset += chunk {
				_, refusal = scanner.Write([]byte(text[offset:min(offset+chunk, len(text))]))
				if refusal != nil {
					break
				}
			}
			var detail *proto.Error
			if !errors.As(refusal, &detail) || detail.Code != "image_secret" || !strings.Contains(detail.Message, "TOKEN") || strings.Contains(fmt.Sprint(refusal), string(secret)) {
				t.Fatal("the original value escaped a streaming scan or entered a diagnostic")
			}
			scanner.clear()
		}
	}
}

func TestSavedRootScannerResetsAtFileBoundariesAndRejectsOnlyCompleteValues(t *testing.T) {
	secret := api.Secret("stable-current-secret-0987654321")
	scanner, err := newSaveScanner(map[string]api.Secret{"TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.clear()
	scanner.reset("/first")
	if _, err := scanner.Write([]byte(string(secret[:10]))); err != nil {
		t.Fatal(err)
	}
	scanner.reset("/second")
	if _, err := scanner.Write([]byte(string(secret[10:]))); err != nil {
		t.Fatal("two different files fabricated one secret", err)
	}
	scanner.reset("/third")
	changed := []byte("xx" + string(secret) + "tail")
	changed[15] ^= 1
	if _, err := scanner.Write([]byte(base64.StdEncoding.EncodeToString(changed))); err != nil {
		t.Fatal("a changed value matched a discarded base64 bit", err)
	}
}

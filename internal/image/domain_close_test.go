package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageReceiptCloseFailureReportsErrorAndPreservesCommittedName(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			imageDomainHome(t)
			store := imageDomainStore(t, imageDomainFixture("a", "b"))
			root, err := store.ownershipRoot()
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			failure := errors.New("injected error after actual metadata descriptor close")
			previous := closeImageMetadata
			t.Cleanup(func() { closeImageMetadata = previous })
			injected := false
			closeImageMetadata = func(file *os.File) error {
				info, statErr := file.Stat()
				closeErr := previous(file)
				selected := kind == "directory" && statErr == nil && info.IsDir() || kind == "file" && strings.HasPrefix(filepath.Base(file.Name()), ".receipt-new-")
				if selected {
					injected = true
					return errors.Join(closeErr, failure)
				}
				return closeErr
			}
			data := []byte(`{"version":1,"fixture":"committed"}`)
			err = replaceReceipt(root, "fixture.json", data)
			if !errors.Is(err, failure) || !injected {
				t.Fatalf("close failure became durable success: %v, injected %v", err, injected)
			}
			actual, _, readErr := readReceipt(root, "fixture.json")
			if readErr != nil || !bytes.Equal(actual, append(data, '\n')) {
				t.Fatalf("close error erased committed publication: %s, %v", actual, readErr)
			}
		})
	}
}

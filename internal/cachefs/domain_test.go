package cachefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func domainFixture(t *testing.T) (*os.Root, *os.File, Domain) {
	t.Helper()
	root, err := OpenPrivateRoot(filepath.Join(t.TempDir(), "store"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	file, err := OpenFile(root, ".lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	held, err := TryLock(file, Exclusive)
	if err != nil || !held {
		t.Fatal("fixture could not hold its actual native sentinel", err)
	}
	return root, file, Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}
}

func domainContents(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func TestDomainCodecRefusesIncompleteFutureDuplicateAndNonCanonicalOwners(t *testing.T) {
	good := `{"scope":"` + strings.Repeat("a", 64) + `","host_id":"` + strings.Repeat("b", 32) + `"}`
	var parsed Domain
	if err := json.Unmarshal([]byte(good), &parsed); err != nil || !parsed.Known() || parsed.IsZero() {
		t.Fatal("canonical owner refused", parsed, err)
	}
	for _, raw := range []string{
		`null`, `{}`, `{"scope":""}`, strings.Replace(good, `"host_id":`, `"future":true,"host_id":`, 1),
		strings.Replace(good, `"scope":`, `"scope":"`+strings.Repeat("c", 64)+`","scope":`, 1),
		strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		strings.Replace(good, strings.Repeat("b", 32), strings.Repeat("b", 31), 1),
	} {
		var domain Domain
		err := json.Unmarshal([]byte(raw), &domain)
		var typed *proto.Error
		if !errors.As(err, &typed) || typed.Code != "cache_domain" || typed.Hint == "" {
			t.Fatalf("invalid owner lost its typed refusal/Try hint: %q: %v", raw, err)
		}
	}
}

func TestDomainObjectBoundaryPreservesEscapedDuplicateAliasAndCancellationRefusals(t *testing.T) {
	original := Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}
	good := `{"scope":"` + original.Scope + `","host_id":"` + original.HostID + `"}`
	for _, raw := range []string{
		strings.Replace(good, `"scope":`, `"sc\u006fpe":"`+original.Scope+`","scope":`, 1),
		strings.Replace(good, `"scope":`, `"Scope":`, 1),
		strings.Replace(good, `"host_id":`, `"host_id":null,"extra":`, 1),
		`[` + good + `]`,
	} {
		domain := original
		err := json.Unmarshal([]byte(raw), &domain)
		var typed *proto.Error
		if !errors.As(err, &typed) || typed.Code != "cache_domain" || typed.Hint == "" || domain != original {
			t.Fatalf("invalid object changed its original value or lost the refusal: %q %v", raw, err)
		}
	}
	if _, err := decodeDomainContext(t.Context(), []byte(good+`{}`)); err == nil {
		t.Fatal("the native domain decoder accepted trailing input")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := decodeDomainContext(ctx, []byte(good)); !errors.Is(err, context.Canceled) {
		t.Fatal("domain decoding lost its original cancellation", err)
	}
}

func TestDomainBindingRefusesNestedDuplicatesAndTrailingInputWithoutRepair(t *testing.T) {
	root, file, expected := domainFixture(t)
	good := `{"version":1,"domain":{"scope":"` + expected.Scope + `","host_id":"` + expected.HostID + `"}}`
	for _, raw := range []string{
		strings.Replace(good, `"version":1`, `"version":1,"vers\u0069on":1`, 1),
		strings.Replace(good, `"scope":`, `"scope":"`+expected.Scope+`","scope":`, 1),
		strings.Replace(good, `"host_id":`, `"Host_id":`, 1),
		good + `{}`,
	} {
		if err := file.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte(raw), 0); err != nil {
			t.Fatal(err)
		}
		if _, found, err := ReadDomainBinding(t.Context(), root, ".lock", file); err == nil || found {
			t.Fatal("a malformed binding acquired an owner", err)
		}
		actual, err := domainContents(root, ".lock")
		if err != nil || string(actual) != raw {
			t.Fatal("refusal changed the original binding bytes", err)
		}
	}
}

func TestDomainBindingSyncsAndRereadsSameNativeSentinel(t *testing.T) {
	root, file, expected := domainFixture(t)
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadDomainBinding(t.Context(), root, ".lock", file); err != nil || found {
		t.Fatal("empty sentinel supplied authority", found, err)
	}
	reference, err := InitialiseDomainBinding(t.Context(), root, ".lock", file, expected)
	if err != nil {
		t.Fatal(err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() <= 0 || !reference.Known() {
		t.Fatal("binding replaced its sentinel or returned no native snapshot", err)
	}
	if err := reference.CheckFile(root, ".lock", file, 0o600); err != nil {
		t.Fatal(err)
	}
	actual, found, err := ReadDomainBinding(t.Context(), root, ".lock", file)
	if err != nil || !found || actual != expected {
		t.Fatal("synced domain did not survive descriptor reread", actual, found, err)
	}
	beforeBytes, err := domainContents(root, ".lock")
	if err != nil {
		t.Fatal(err)
	}
	other := Domain{Scope: strings.Repeat("c", 64), HostID: expected.HostID}
	if _, err := InitialiseDomainBinding(t.Context(), root, ".lock", file, other); err == nil {
		t.Fatal("another home rebound the populated sentinel")
	}
	afterBytes, err := domainContents(root, ".lock")
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("refused rebinding changed stored owner bytes", err)
	}
}

func TestDomainBindingPreservesPartialAndFutureMetadata(t *testing.T) {
	for _, contents := range []string{`{"version":`, `{"version":9,"domain":{}}`, `{"version":1,"future":true,"domain":{}}`, "unrelated owned file contents"} {
		t.Run(contents, func(t *testing.T) {
			root, file, domain := domainFixture(t)
			if _, err := file.WriteAt([]byte(contents), 0); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadDomainBinding(t.Context(), root, ".lock", file); err == nil {
				t.Fatal("unreadable or future sentinel was treated as unbound")
			}
			if _, err := InitialiseDomainBinding(t.Context(), root, ".lock", file, domain); err == nil {
				t.Fatal("unknown nonempty metadata was truncated or adopted")
			}
			actual, err := domainContents(root, ".lock")
			if err != nil || string(actual) != contents {
				t.Fatal("unknown sentinel content changed", err)
			}
		})
	}
}

func TestDomainBindingRejectsReplacedAndLinkedSentinelDescriptors(t *testing.T) {
	for _, linked := range []bool{false, true} {
		root, file, domain := domainFixture(t)
		if err := root.Rename(".lock", ".retained"); err != nil {
			t.Fatal(err)
		}
		if linked {
			if err := root.Symlink(".retained", ".lock"); err != nil {
				t.Fatal(err)
			}
		} else {
			replacement, err := root.OpenFile(".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := replacement.Write([]byte("replacement"))
			closeErr := replacement.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatal(writeErr, closeErr)
			}
		}
		if _, err := InitialiseDomainBinding(t.Context(), root, ".lock", file, domain); err == nil {
			t.Fatal("a stale descriptor created a new owner domain")
		}
		actual, err := domainContents(root, ".retained")
		if err != nil || len(actual) != 0 {
			t.Fatal("refusal wrote through the old unlinked lock domain", err)
		}
	}
}

func TestDomainBindingCancellationDoesNotWriteOwner(t *testing.T) {
	root, file, domain := domainFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InitialiseDomainBinding(ctx, root, ".lock", file, domain); !errors.Is(err, context.Canceled) {
		t.Fatal("binding cancellation identity lost", err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatal("cancelled binding changed the empty sentinel", err)
	}
}

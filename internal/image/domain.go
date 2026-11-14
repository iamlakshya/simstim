package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// OpenHost opens host image storage for one pinned placement domain. The host
// owns the operation context and supplies the final publication admission.
func OpenHost(ctx context.Context, dir string, domain cachefs.Domain, gate PublicationGate) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, err
	}
	if gate == nil {
		return nil, imageDomainError("host image publication has no final admission gate")
	}
	return openDomainStore(ctx, dir, domain, gate, true)
}

// OpenHostExisting anchors existing storage for this exact host domain. It
// creates no root, infrastructure, lease or ownership receipt.
func OpenHostExisting(ctx context.Context, dir string, domain cachefs.Domain) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, err
	}
	return openDomainStore(ctx, dir, domain, nil, false)
}

func openDomainStore(ctx context.Context, dir string, domain cachefs.Domain, gate PublicationGate, create bool) (*Store, error) {
	var store *Store
	entered := false
	open := func() error {
		if entered {
			return imageDomainError("the constructor admission invoked its callback more than once")
		}
		entered = true
		if err := ctx.Err(); err != nil {
			return err
		}
		root, err := cachefs.OpenPrivateRoot(dir, create)
		if err != nil {
			return err
		}
		store = &Store{root: root, domain: domain, gate: gate, receipts: create, existingOnly: !create}
		if domain.IsZero() {
			if err := store.refuseHostRecords(); err != nil {
				root.Close()
				store = nil
				return err
			}
		}
		if create {
			for _, name := range []string{"incoming", "ownership"} {
				if err := store.mkdir(name); err != nil {
					root.Close()
					store = nil
					return err
				}
			}
		}
		store.generation, err = cachefs.CaptureRoot(root)
		if err != nil {
			root.Close()
			store = nil
		}
		return err
	}
	var err error
	if create && !domain.IsZero() {
		err = gate(ctx, open)
	} else {
		err = open()
	}
	if err != nil {
		if store != nil {
			err = errors.Join(err, store.Close())
		}
		return nil, err
	}
	if store == nil {
		return nil, imageDomainError("the constructor admission did not open the store")
	}
	if err := ctx.Err(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func imageDomainError(message string) error {
	return &proto.Error{Code: "cache_domain", Message: "image storage domain refused: " + message, Hint: "Restore the recorded host roots or use disjoint storage. Preserve legacy, unknown and other-domain objects."}
}

func (s *Store) receiptDomain() *cachefs.Domain {
	if s.domain.IsZero() {
		return nil
	}
	domain := s.domain
	return &domain
}

func (s *Store) checkDomain(domain *cachefs.Domain) error {
	if s.domain.IsZero() {
		if domain != nil {
			return imageDomainError("a fixture or laptop operation cannot use a host-bound object")
		}
		return nil
	}
	if err := s.domain.Validate(); err != nil {
		return err
	}
	if domain == nil || !domain.Known() || *domain != s.domain {
		return imageDomainError("the object has no receipt for this exact scope and checked host identity")
	}
	return nil
}

// This constructor scan only refuses. It supplies no read, replay or mutation
// authority and does not replace the later receipt check under its lease.
func (s *Store) refuseHostRecords() error {
	root, err := s.ownershipRoot()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "image-") && !strings.HasPrefix(name, "lease-") && !strings.HasPrefix(name, ".receipt-") && name != "agents-directory.json" {
			continue
		}
		data, _, err := readReceipt(root, name)
		if err != nil {
			return err
		}
		// Both canonical receipt codecs recognise the same optional Domain.
		// Neither successful decoding nor a familiar name grants authority.
		var receipt imageOwnership
		if err := json.Unmarshal(data, &receipt); err != nil {
			return err
		}
		if receipt.Domain != nil {
			return imageDomainError("the existing root contains host-bound receipt metadata")
		}
	}
	return nil
}

// leaseRole is closed. Other features add their reviewed resource roles here,
// rather than treating arbitrary clean relative paths as lock authority.
func leaseRole(name string) (string, bool) {
	if name == "agents.lock" {
		return "agents-source", true
	}
	if strings.HasSuffix(name, ".lock") {
		key := strings.TrimSuffix(name, ".lock")
		if supply.IsHex64(key) {
			return "platform", true
		}
		if strings.HasPrefix(key, "incoming/") && supply.IsHex64(strings.TrimPrefix(key, "incoming/")) {
			return "incoming", true
		}
		if strings.HasPrefix(key, "agents/") && supply.IsHex64(strings.TrimPrefix(key, "agents/")) {
			return "agents-disk", true
		}
	}
	return "", false
}

func (s *Store) checkLeaseOwner(ctx context.Context, name string, file *os.File) error {
	receipt, err := s.readLeaseOwnership(leaseReceiptName(name))
	if errors.Is(err, os.ErrNotExist) && s.domain.IsZero() {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return imageDomainError("the existing lease has no exact domain ownership receipt")
	}
	if err != nil {
		return err
	}
	if receipt.Path != name || receipt.Quarantine != "" || receipt.Cleanup != "" {
		return ownershipError("the lease has no current receipt at its original name")
	}
	parent, leaf, err := s.parent(name)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := receipt.Parent.CheckRoot(parent); err != nil {
		return err
	}
	if err := receipt.File.CheckFile(parent, leaf, file, 0o600); err != nil {
		return err
	}
	if role, _ := leaseRole(name); role == "platform" && !s.domain.IsZero() {
		return s.checkPlatformObject(strings.TrimSuffix(name, ".lock"))
	}
	if role, _ := leaseRole(name); role == "agents-source" || role == "agents-disk" {
		return s.checkAgentsLeaseObject(ctx, name)
	}
	return nil
}

func (s *Store) checkPlatformObject(hex string) error {
	_, err := s.root.Lstat(hex)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := s.root.Lstat("ownership/" + imageReceiptName("sha256:"+hex)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := s.readOwnership("sha256:" + hex); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return imageDomainError("a populated host image has no exact domain receipt")
		}
		return err
	}
	return nil
}

func (s *Store) permitLeaseCreation(ctx context.Context, name string) error {
	if s.domain.IsZero() {
		return nil
	}
	if _, err := s.root.Lstat("ownership/" + leaseReceiptName(name)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return imageDomainError("the absent lease still has a recorded native generation")
	}
	if role, _ := leaseRole(name); role == "platform" {
		hex := strings.TrimSuffix(name, ".lock")
		for _, leaf := range []string{hex, "ownership/" + imageReceiptName("sha256:"+hex)} {
			if _, err := s.root.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
				if err != nil {
					return err
				}
				return imageDomainError("an existing image cannot acquire a replacement lease generation")
			}
		}
	}
	if role, _ := leaseRole(name); role == "agents-source" || role == "agents-disk" {
		return s.permitAgentsLeaseCreation(ctx, name)
	}
	return nil
}

func (s *Store) existingReceiptLease(ctx context.Context, receipt leaseOwnership, actual string) (*Lease, error) {
	if !receipt.known() {
		return nil, ownershipError("captured lease has no canonical original resource receipt")
	}
	if err := s.checkDomain(receipt.Domain); err != nil {
		return nil, err
	}
	if err := receipt.Store.CheckRoot(s.root); err != nil {
		return nil, err
	}
	if actual != receipt.Path {
		parent := ""
		if directory := path.Dir(receipt.Path); directory != "." {
			parent = directory + "/"
		}
		if receipt.Quarantine == "" || actual != parent+receipt.Quarantine {
			return nil, ownershipError("captured lease name differs from its exact receipt")
		}
	}
	file, err := s.regular(actual, os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return s.waitLease(ctx, actual, file, false, &receipt, false)
}

func (s *Store) requireWritableHost() error {
	if !s.domain.IsZero() && (s.existingOnly || s.gate == nil) {
		return imageDomainError("existing host storage permits inventory and owned purge, not new publication")
	}
	return nil
}

func (s *Store) withCreation(ctx context.Context, create func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.domain.IsZero() {
		return create()
	}
	return s.withPublication(ctx, create)
}

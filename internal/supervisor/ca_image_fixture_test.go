package supervisor

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
)

const maxTestImageTrustBytes = 8 << 20

func testPublicRoots(data []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	count := 0
	for len(bytes.TrimSpace(data)) != 0 {
		data = bytes.TrimSpace(data)
		if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("test origin trust contains non-certificate material")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("test origin trust must contain only public certificate PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("test origin trust contains an invalid certificate")
		}
		pool.AddCert(cert)
		count++
		data = rest
	}
	if count == 0 {
		return nil, errors.New("test origin trust contains no public certificates")
	}
	return pool, nil
}

// The existing test subprocess entry never falls back to a user's keychain.
// This variable is a path to an owned public PEM file, not literal PEM text.
func configureTestEgressRoots(opt *egress.Options) error {
	opt.OriginRoots = x509.NewCertPool()
	path := os.Getenv("TEST_EGRESS_ORIGIN_CA")
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open the owned test origin trust file")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxTestImageTrustBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxTestImageTrustBytes {
		return errors.New("cannot read the bounded owned test origin trust file")
	}
	pool, err := testPublicRoots(data)
	if err != nil {
		return err
	}
	opt.OriginRoots = pool
	return nil
}

func newTestPublicImageRoot() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Owned image authority"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func prepareTestGuestImageTrust() error {
	root := os.Getenv(guest.RootEnv)
	// A real namespace image owns its existing system bundle. This test-only
	// preparation applies only to the plain driver's explicit owned root.
	if root == "" {
		return nil
	}
	if !filepath.IsAbs(root) {
		return errors.New("test guest has no owned image root")
	}
	public, err := newTestPublicImageRoot()
	if err != nil {
		return err
	}
	return installTestGuestImageRoots(root, public)
}

func installTestGuestImageRoots(path string, public []byte) error {
	if _, err := testPublicRoots(public); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll("etc/ssl/certs", 0o700); err != nil {
		return err
	}
	certs, err := root.OpenRoot("etc/ssl/certs")
	if err != nil {
		return err
	}
	defer certs.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".fixture-roots-" + hex.EncodeToString(random[:])
	file, err := certs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = certs.Remove(name) }()
	if _, err := file.Write(public); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return certs.Rename(name, "ca-certificates.crt")
}

func TestEgressEntryOwnedPublicRootsNeverFallBackToSystemTrust(t *testing.T) {
	t.Setenv("TEST_EGRESS_ORIGIN_CA", "")
	var empty egress.Options
	if err := configureTestEgressRoots(&empty); err != nil || empty.OriginRoots == nil || !empty.OriginRoots.Equal(x509.NewCertPool()) {
		t.Fatal("test egress entry did not explicitly install an empty origin pool")
	}
	public, err := newTestPublicImageRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		good bool
	}{
		{"public", public, true},
		{"empty", nil, false},
		{"not-pem", []byte("not a certificate"), false},
		{"leading-junk", append([]byte("unexpected data\n"), public...), false},
		{"trailing-junk", append(append([]byte(nil), public...), []byte("unexpected data")...), false},
		{"other-block", pem.EncodeToMemory(&pem.Block{Type: "NOT A CERTIFICATE", Bytes: []byte("public fixture")}), false},
		{"over-limit", []byte(strings.Repeat("x", maxTestImageTrustBytes+1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owned-public-roots.pem")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_EGRESS_ORIGIN_CA", path)
			var opt egress.Options
			err := configureTestEgressRoots(&opt)
			if (err == nil) != tc.good || opt.OriginRoots == nil {
				t.Fatal("owned public-file validation did not preserve its declared boundary")
			}
			if tc.good && opt.OriginRoots.Equal(x509.NewCertPool()) {
				t.Fatal("valid public roots did not enter the owned pool")
			}
		})
	}
}

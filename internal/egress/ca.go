package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"time"
)

const maxCachedLeaves = 128

// sessionAuthority owns signing material in the egress process alone. There is
// deliberately no key export or serialisation method. Only publicPEM crosses
// the inherited control link to the guest's trust store.
type sessionAuthority struct {
	mu        sync.Mutex
	key       *ecdsa.PrivateKey
	cert      *x509.Certificate
	publicPEM []byte
	leaves    map[string]*tls.Certificate
	order     []string
}

func newSessionAuthority(now time.Time) (*sessionAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate session CA: %w", err)
	}
	serial, err := certificateSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Session network authority"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sign session CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse session CA: %w", err)
	}
	return &sessionAuthority{key: key, cert: cert,
		publicPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves:    make(map[string]*tls.Certificate)}, nil
}

func certificateSerial() (*big.Int, error) {
	// x509 requires a positive serial. Sampling below 2^128 leaves ample
	// uniqueness while staying inside the standard's 20-octet limit.
	for {
		n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, fmt.Errorf("generate certificate serial: %w", err)
		}
		if n.Sign() > 0 {
			return n, nil
		}
	}
}

func (a *sessionAuthority) certificate(host string, now time.Time) (*tls.Certificate, error) {
	canonical, err := NormaliseHost(host)
	if err != nil || canonical != host {
		return nil, errors.New("certificate requires a canonical destination host")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !now.Before(a.cert.NotAfter) {
		return nil, errors.New("session CA has expired. Try: stop and resume the session")
	}
	if leaf := a.leaves[host]; leaf != nil && now.Add(time.Minute).Before(leaf.Leaf.NotAfter) {
		return leaf, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate destination certificate key: %w", err)
	}
	serial, err := certificateSerial()
	if err != nil {
		return nil, err
	}
	until := now.Add(24 * time.Hour)
	if a.cert.NotAfter.Before(until) {
		until = a.cert.NotAfter
	}
	template := &x509.Certificate{SerialNumber: serial,
		Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-5 * time.Minute),
		NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		template.IPAddresses = []net.IP{net.IP(ip.AsSlice())}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("sign destination certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse destination certificate: %w", err)
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key, Leaf: cert}
	if _, exists := a.leaves[host]; !exists {
		if len(a.order) == maxCachedLeaves {
			delete(a.leaves, a.order[0])
			a.order = a.order[1:]
		}
		a.order = append(a.order, host)
	}
	a.leaves[host] = leaf
	return leaf, nil
}

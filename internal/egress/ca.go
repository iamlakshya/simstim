package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

const (
	// maxCachedLeaves bounds the per-session leaf certificate cache so a busy
	// session cannot grow memory without limit.
	maxCachedLeaves = 512

	caCertBackdate = 5 * time.Minute
	caCertLifetime = 24 * time.Hour
)

// SessionCA holds an ephemeral ECDSA P-256 certificate authority for one
// walled session (decision 0032, decision 0038, spec 7.2). Its private key
// lives only in the egress process's memory and is never written to disk or
// transmitted over fd 3.
type SessionCA struct {
	mu      sync.RWMutex
	cert    *x509.Certificate
	certDER []byte
	certPEM string
	key     *ecdsa.PrivateKey
	leaves  map[string]*tls.Certificate
	closed  bool
}

// NewSessionCA generates a fresh per-session ECDSA P-256 CA in memory.
func NewSessionCA() (*SessionCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("egress ca: generate CA key: %w. Try: check host entropy availability", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ski := subjectKeyID(&key.PublicKey)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   brand.Name + " session CA",
			Organization: []string{brand.Name},
		},
		NotBefore:             now.Add(-caCertBackdate),
		NotAfter:              now.Add(caCertLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("egress ca: sign CA certificate: %w. Try: retry starting the session", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("egress ca: parse CA certificate: %w. Try: retry starting the session", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	})
	return &SessionCA{
		cert:    parsed,
		certDER: der,
		certPEM: string(pemBytes),
		key:     key,
		leaves:  make(map[string]*tls.Certificate),
	}, nil
}

// CertPEM returns the CA's public certificate in PEM form. It never contains
// private key material.
func (ca *SessionCA) CertPEM() string {
	if ca == nil {
		return ""
	}
	return ca.certPEM
}

// CertPool returns an x509.CertPool trusting only this session's CA certificate.
func (ca *SessionCA) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	if ca != nil && ca.cert != nil {
		pool.AddCert(ca.cert)
	}
	return pool
}

// LeafForHost returns a cached (or newly signed) ECDSA P-256 server leaf
// certificate for rawHost signed by this session CA.
func (ca *SessionCA) LeafForHost(rawHost string) (*tls.Certificate, error) {
	if ca == nil {
		return nil, errors.New("egress ca: session CA is nil. Try: create the server with NewServer")
	}
	host, err := normalizeHost(rawHost)
	if err != nil {
		return nil, fmt.Errorf("egress ca: leaf host %q: %w", rawHost, err)
	}

	ca.mu.RLock()
	if ca.closed {
		ca.mu.RUnlock()
		return nil, errors.New("egress ca: session CA is closed. Try: restart the session")
	}
	if leaf, ok := ca.leaves[host]; ok {
		ca.mu.RUnlock()
		return leaf, nil
	}
	ca.mu.RUnlock()

	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.closed {
		return nil, errors.New("egress ca: session CA is closed. Try: restart the session")
	}
	if leaf, ok := ca.leaves[host]; ok {
		return leaf, nil
	}

	leaf, err := ca.mintLeafLocked(host)
	if err != nil {
		return nil, err
	}
	if len(ca.leaves) >= maxCachedLeaves {
		ca.leaves = make(map[string]*tls.Certificate)
	}
	ca.leaves[host] = leaf
	return leaf, nil
}

func (ca *SessionCA) mintLeafLocked(host string) (*tls.Certificate, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("egress ca: generate leaf key for %s: %w. Try: check host entropy availability", host, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-caCertBackdate),
		NotAfter:              now.Add(caCertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          subjectKeyID(&leafKey.PublicKey),
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		tmpl.IPAddresses = []net.IP{addr.AsSlice()}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("egress ca: sign leaf certificate for %s: %w. Try: retry the request", host, err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("egress ca: parse leaf certificate for %s: %w. Try: retry the request", host, err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, ca.certDER},
		PrivateKey:  leafKey,
		Leaf:        parsed,
	}, nil
}

// TLSConfigForHost builds a server tls.Config for an intercepted host. It
// enforces CheckClientHello (SNI matches expectedHost, no ECH) before issuing
// or presenting a leaf certificate, and offers HTTP/2 via ALPN when offerHTTP2
// is true (spec 7.2).
func (ca *SessionCA) TLSConfigForHost(expectedHost string, offerHTTP2 bool) *tls.Config {
	nextProtos := []string{"http/1.1"}
	if offerHTTP2 {
		nextProtos = []string{"h2", "http/1.1"}
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: nextProtos,
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			ch := ClientHello{
				ServerName: chi.ServerName,
				Extensions: slices.Clone(chi.Extensions),
				HasECH: slices.Contains(chi.Extensions, extEncryptedClientHello) ||
					slices.Contains(chi.Extensions, extECHOuterExtensions),
			}
			if err := CheckClientHello(ch, expectedHost); err != nil {
				return nil, err
			}
			leaf, err := ca.LeafForHost(expectedHost)
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{*leaf},
				NextProtos:   nextProtos,
			}, nil
		},
	}
}

// Close discards the in-memory CA private key and cached leaves.
func (ca *SessionCA) Close() {
	if ca == nil {
		return
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.closed = true
	ca.key = nil
	ca.leaves = nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("egress ca: generate serial number: %w. Try: check host entropy availability", err)
	}
	if n.Sign() <= 0 {
		n.SetInt64(1)
	}
	return n, nil
}

func subjectKeyID(pub *ecdsa.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(der)
	return sum[:20]
}

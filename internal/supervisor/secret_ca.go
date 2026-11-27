package supervisor

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"

	"github.com/iamlakshya/simstim/internal/proto"
)

func publicCAFailure() error {
	return &proto.Error{Code: "egress_ca", Message: "egress supplied no valid public session certificate",
		Hint: "Use matching helpers, then stop and resume the sandbox."}
}

// validatePublicSessionCA accepts only the canonical sole certificate block
// emitted by the owned egress. PEM preambles/headers cannot smuggle values.
func validatePublicSessionCA(raw string) error {
	encoded := []byte(raw)
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 {
		return publicCAFailure()
	}
	canonical := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
	if !bytes.Equal(encoded, canonical) {
		return publicCAFailure()
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return publicCAFailure()
	}
	if certificate.CheckSignatureFrom(certificate) != nil {
		return publicCAFailure()
	}
	return nil
}

package supply

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
)

//go:embed trusted_root.json
var embeddedSigstoreRoot []byte

// SigstoreRoot returns an owned copy of the embedded public Sigstore root.
// Verification never refreshes this root or contacts a transparency service.
func SigstoreRoot() []byte { return bytes.Clone(embeddedSigstoreRoot) }

// ImagePolicyHash binds accepted signatures to their exact identity, issuer,
// root selection and root bytes. A changed path or its contents checks again.
func ImagePolicyHash(identity, issuer, rootSelection string, trustedRoot []byte) string {
	hash := sha256.New()
	for _, value := range [][]byte{[]byte("image-policy-v1"), []byte(identity), []byte(issuer), []byte(rootSelection), trustedRoot} {
		fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write(value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

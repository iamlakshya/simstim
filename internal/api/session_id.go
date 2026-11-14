package api

import "crypto/rand"

// RandomSessionID makes an eight-character session ID from cryptographic
// randomness. Every ID it makes includes a digit.
func RandomSessionID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	out := make([]byte, len(random))
	for i, b := range random {
		out[i] = SessionIDAlphabet[int(b)%len(SessionIDAlphabet)]
	}
	out[0] = "234567"[int(random[0])%6]
	return string(out), nil
}

package egress

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2/hpack"
)

const physicalH2TokenHeader = "X-Internal-Http-Delivery"
const physicalH2TokenLength = 32 + 1 + 8 + 1 + 64
const physicalH2TokenOverhead = len(physicalH2TokenHeader) + physicalH2TokenLength + 32

var errPhysicalH2Binding = errors.New("HTTP delivery has no original connection binding. Try: reconnect with matching helpers")

// Private connection authentication describes delivery only. It has no target,
// permit, Source constructor, credential lookup or policy capability.
type physicalH2TokenKey struct {
	generation [16]byte
	secret     [32]byte
}

func (*physicalH2TokenKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newPhysicalH2TokenKey() (*physicalH2TokenKey, error) {
	key := new(physicalH2TokenKey)
	if _, err := io.ReadFull(rand.Reader, key.generation[:]); err != nil {
		return nil, errPhysicalH2Binding
	}
	if _, err := io.ReadFull(rand.Reader, key.secret[:]); err != nil {
		return nil, errPhysicalH2Binding
	}
	return key, nil
}

func (key *physicalH2TokenKey) token(stream uint32) (string, error) {
	if key == nil || stream == 0 || stream&1 == 0 || stream > 0x7fffffff {
		return "", errPhysicalH2Binding
	}
	prefix := hex.EncodeToString(key.generation[:]) + "." + fmt.Sprintf("%08x", stream)
	mac := hmac.New(sha256.New, key.secret[:])
	_, _ = io.WriteString(mac, prefix)
	return prefix + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

// Consume removes only the final appended value. Every original guest value,
// including duplicates under this name, remains exactly as net/http parsed it.
func (key *physicalH2TokenKey) consume(request *http.Request) (uint32, error) {
	if request == nil {
		return 0, errPhysicalH2Binding
	}
	values := request.Header.Values(physicalH2TokenHeader)
	if len(values) == 0 {
		return 0, errPhysicalH2Binding
	}
	value := values[len(values)-1]
	if len(value) != physicalH2TokenLength || value[32] != '.' || value[41] != '.' {
		return 0, errPhysicalH2Binding
	}
	parsed, err := strconv.ParseUint(value[33:41], 16, 32)
	if err != nil {
		return 0, errPhysicalH2Binding
	}
	stream := uint32(parsed)
	expected, err := key.token(stream)
	if err != nil || !hmac.Equal([]byte(value), []byte(expected)) {
		return 0, errPhysicalH2Binding
	}
	if len(values) == 1 {
		request.Header.Del(physicalH2TokenHeader)
	} else {
		request.Header[http.CanonicalHeaderKey(physicalH2TokenHeader)] = values[:len(values)-1]
	}
	return stream, nil
}

func (key *physicalH2TokenKey) literal(stream uint32) ([]byte, error) {
	value, err := key.token(stream)
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	// A fresh encoder has no dynamic name reference; Sensitive makes this
	// literal never indexed, so the guest's original table stays unchanged.
	encoder := hpack.NewEncoder(&encoded)
	if err := encoder.WriteField(hpack.HeaderField{Name: strings.ToLower(physicalH2TokenHeader), Value: value, Sensitive: true}); err != nil {
		return nil, errPhysicalH2Binding
	}
	return encoded.Bytes(), nil
}

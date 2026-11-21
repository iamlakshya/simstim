//go:build linux

package jail

import (
	"bytes"
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestVMMUnixPeerReplyRequiresExactNativePairV1(t *testing.T) {
	raw := make([]byte, 40)
	native := binary.NativeEndian
	native.PutUint32(raw[:4], 40)
	native.PutUint16(raw[4:6], unix.SOCK_DIAG_BY_FAMILY)
	native.PutUint32(raw[8:12], 1)
	native.PutUint32(raw[12:16], 17)
	raw[16], raw[17] = unix.AF_UNIX, unix.SOCK_STREAM
	native.PutUint32(raw[20:24], 41)
	native.PutUint16(raw[32:34], 8)
	native.PutUint16(raw[34:36], 3)
	native.PutUint32(raw[36:40], 42)
	if peer, err := parseVMMUnixPeerV1(raw, 41, 17); err != nil || peer != 42 {
		t.Fatalf("native peer parse: %d, %v", peer, err)
	}
	for _, change := range []func([]byte) []byte{
		func(data []byte) []byte { return data[:39] },
		func(data []byte) []byte { native.PutUint32(data[8:12], 2); return data },
		func(data []byte) []byte { native.PutUint32(data[12:16], 18); return data },
		func(data []byte) []byte { native.PutUint32(data[20:24], 43); return data },
		func(data []byte) []byte { native.PutUint16(data[32:34], 3); return data },
		func(data []byte) []byte { native.PutUint32(data[36:40], 0); return data },
		func(data []byte) []byte {
			data = append(data, data[32:]...)
			native.PutUint32(data[:4], uint32(len(data)))
			return data
		},
	} {
		if _, err := parseVMMUnixPeerV1(change(bytes.Clone(raw)), 41, 17); err == nil {
			t.Fatal("native pair diagnostic substitution was accepted")
		}
	}
}

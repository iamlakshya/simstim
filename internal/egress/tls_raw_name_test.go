package egress

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadClientHelloPreservesRawSNIThroughExtensionParser(t *testing.T) {
	const original = "MiXeD.example.com."
	name := []byte{0, 0, 0}
	binary.BigEndian.PutUint16(name[1:], uint16(len(original)))
	name = append(name, original...)
	list := make([]byte, 2)
	binary.BigEndian.PutUint16(list, uint16(len(name)))
	list = append(list, name...)
	extension := make([]byte, 4)
	binary.BigEndian.PutUint16(extension[2:], uint16(len(list)))
	extension = append(extension, list...)
	body := append([]byte{3, 3}, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0x13, 1, 1, 0)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(extension)))
	body = append(body, length...)
	body = append(body, extension...)
	handshake := []byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	handshake = append(handshake, body...)
	record := []byte{0x16, 3, 3, 0, 0}
	binary.BigEndian.PutUint16(record[3:], uint16(len(handshake)))
	record = append(record, handshake...)
	parsed, err := ReadClientHello(bytes.NewReader(record))
	if err != nil || parsed.RawServerName != original || parsed.ServerName != "mixed.example.com" || !bytes.Equal(parsed.Raw, record) {
		t.Fatal("raw SNI was dropped or normalised before the public-stand-in tripwire")
	}
	if err := CheckClientHello(parsed, "mixed.example.com"); err != nil {
		t.Fatal("raw retention changed the existing canonical SNI matcher")
	}
}

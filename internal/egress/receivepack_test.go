package egress

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

const receiveTestPrefix = "refs/heads/project/session/"

func receiveTestPacket(line string) []byte { return fmt.Appendf(nil, "%04x%s", len(line)+4, line) }

func receiveTestCommand(ref string, width int, deleted bool) string {
	old, newID := strings.Repeat("a", width), strings.Repeat("b", width)
	if deleted {
		newID = strings.Repeat("0", width)
	}
	return old + " " + newID + " " + ref
}

func TestReceivePackKeepsCheckedPrefixExactAndLeavesPackUnread(t *testing.T) {
	for _, width := range []int{40, 64} {
		format := "sha1"
		if width == 64 {
			format = "sha256"
		}
		first := receiveTestCommand(receiveTestPrefix+"fix", width, false) + "\x00report-status object-format=" + format + "\n"
		prefix := append(receiveTestPacket(first), receiveTestPacket(receiveTestCommand(receiveTestPrefix+"other", width, false))...)
		prefix = append(prefix, []byte("0000")...)
		pack := []byte{'P', 'A', 'C', 'K', 0, 1, 0, 255}
		input := bytes.NewReader(append(bytes.Clone(prefix), pack...))
		got, err := readReceivePackPrefix(input, receiveTestPrefix)
		if err != nil || !bytes.Equal(got, prefix) {
			t.Fatalf("valid %d-bit-layout prefix changed: %v", width, err)
		}
		left, err := io.ReadAll(input)
		if err != nil || !bytes.Equal(left, pack) {
			t.Fatal("command check consumed or changed pack bytes")
		}
	}
}

func TestReceivePackRefusesDeletesTagsOtherNamespacesAndMalformedCommands(t *testing.T) {
	for _, test := range []struct{ name, line string }{
		{"main", receiveTestCommand("refs/heads/main", 40, false)},
		{"tag", receiveTestCommand("refs/tags/release", 40, false)},
		{"other session", receiveTestCommand("refs/heads/project/another/fix", 40, false)},
		{"prefix alias", receiveTestCommand("refs/heads/project/session-other/fix", 40, false)},
		{"empty suffix", receiveTestCommand(receiveTestPrefix, 40, false)},
		{"delete", receiveTestCommand(receiveTestPrefix+"fix", 40, true)},
		{"dot segment", receiveTestCommand(receiveTestPrefix+"../main", 40, false)},
		{"lock suffix", receiveTestCommand(receiveTestPrefix+"fix.lock", 40, false)},
		{"embedded control", receiveTestCommand(receiveTestPrefix+"fix\r", 40, false)},
		{"wrong hash format", receiveTestCommand(receiveTestPrefix+"fix", 64, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := append(receiveTestPacket(test.line+"\x00report-status"), []byte("0000PACK")...)
			if prefix, err := readReceivePackPrefix(bytes.NewReader(wire), receiveTestPrefix); err == nil || prefix != nil {
				t.Fatal("forbidden reference command produced an admitted prefix")
			}
		})
	}
	first := receiveTestCommand(receiveTestPrefix+"fix", 40, false) + "\x00report-status"
	badSecond := append(receiveTestPacket(first), receiveTestPacket(receiveTestCommand("refs/heads/main", 40, false))...)
	badSecond = append(badSecond, []byte("0000PACK")...)
	if _, err := readReceivePackPrefix(bytes.NewReader(badSecond), receiveTestPrefix); err == nil {
		t.Fatal("a later command escaped whole-prefix validation")
	}
	for _, wire := range [][]byte{[]byte("0001"), []byte("0004"), []byte("ffff"), []byte("0000PACK"), receiveTestPacket(first[:len(first)-2])} {
		if _, err := readReceivePackPrefix(bytes.NewReader(wire), receiveTestPrefix); err == nil {
			t.Fatal("malformed or incomplete pkt-line list passed")
		}
	}
}

func TestReceivePackSignedEnvelopeChecksItsCommands(t *testing.T) {
	// Authored protocol framing only. This is not a valid signature, Git
	// execution or provider traffic recording. The server verifies signatures.
	makeWire := func(ref string) []byte {
		var wire []byte
		for _, line := range []string{"push-cert\x00report-status\n", "certificate version 0.1\n", "pusher Author <author@example.com> 1 +0000\n", "pushee https://example.com/org/repo\n", "nonce authored\n", "\n", receiveTestCommand(ref, 40, false) + "\n", "-----BEGIN PGP SIGNATURE-----\n", "YXV0aG9yZWQ=\n", "-----END PGP SIGNATURE-----\n", "push-cert-end\n"} {
			wire = append(wire, receiveTestPacket(line)...)
		}
		return append(wire, []byte("0000PACK")...)
	}
	if _, err := readReceivePackPrefix(bytes.NewReader(makeWire(receiveTestPrefix+"fix")), receiveTestPrefix); err != nil {
		t.Fatal(err)
	}
	if _, err := readReceivePackPrefix(bytes.NewReader(makeWire("refs/heads/main")), receiveTestPrefix); err == nil {
		t.Fatal("a signed envelope hid a protected ref")
	}
	missingLF := bytes.Replace(makeWire(receiveTestPrefix+"fix"), receiveTestPacket("certificate version 0.1\n"), receiveTestPacket("certificate version 0.1"), 1)
	if _, err := readReceivePackPrefix(bytes.NewReader(missingLF), receiveTestPrefix); err == nil {
		t.Fatal("signed certificate accepted a missing mandatory LF")
	}
	hiddenCommand := bytes.Replace(makeWire(receiveTestPrefix+"fix"), receiveTestPacket("YXV0aG9yZWQ=\n"), receiveTestPacket(receiveTestCommand("refs/heads/main", 40, false)+"\n"), 1)
	if _, err := readReceivePackPrefix(bytes.NewReader(hiddenCommand), receiveTestPrefix); err == nil {
		t.Fatal("signed request treated another reference command as signature data")
	}
}

func TestReceivePackShallowHashNegotiationAndWireLimit(t *testing.T) {
	for _, width := range []int{40, 64} {
		format := "sha1"
		if width == 64 {
			format = "sha256"
		}
		wire := append(receiveTestPacket("shallow "+strings.Repeat("a", width)), receiveTestPacket(receiveTestCommand(receiveTestPrefix+"fix", width, false)+"\x00object-format="+format)...)
		wire = append(wire, []byte("0000PACK")...)
		if _, err := readReceivePackPrefix(bytes.NewReader(wire), receiveTestPrefix); err != nil {
			t.Fatal("matching shallow/object format refused", err)
		}
	}
	wrong := append(receiveTestPacket("shallow "+strings.Repeat("a", 64)), receiveTestPacket(receiveTestCommand(receiveTestPrefix+"fix", 40, false)+"\x00object-format=sha1")...)
	wrong = append(wrong, []byte("0000PACK")...)
	if _, err := readReceivePackPrefix(bytes.NewReader(wrong), receiveTestPrefix); err == nil {
		t.Fatal("shallow hash width disagreed with negotiation")
	}
	if _, err := readReceivePackPrefix(bytes.NewReader([]byte("fff1")), receiveTestPrefix); err == nil {
		t.Fatal("packet exceeded the documented 65520-byte wire limit")
	}
}

func FuzzReceivePackPrefix(f *testing.F) {
	valid := append(receiveTestPacket(receiveTestCommand(receiveTestPrefix+"fix", 40, false)+"\x00report-status"), []byte("0000PACK")...)
	f.Add(valid)
	f.Add([]byte("ffff"))
	f.Add([]byte("0000"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 128<<10 {
			t.Skip()
		}
		original := bytes.Clone(wire)
		prefix, err := readReceivePackPrefix(bytes.NewReader(wire), receiveTestPrefix)
		if !bytes.Equal(wire, original) {
			t.Fatal("inspection changed its input storage")
		}
		if err == nil && !bytes.HasPrefix(wire, prefix) {
			t.Fatal("admitted bytes are not an exact input prefix")
		}
	})
}

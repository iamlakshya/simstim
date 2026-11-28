package client

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestAllowedPasteContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want bool
	}{
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), true},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, true},
		{"gif87a", []byte("GIF87a..."), true},
		{"gif89a", []byte("GIF89a..."), true},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), true},
		{"pdf", []byte("%PDF-1.7\n%"), true},
		{"zip", []byte("PK\x03\x04archive"), false},
		{"plain text", []byte("hello world\n"), false},
		{"shell script", []byte("#!/bin/sh\nid\n"), false},
		{"riff non-webp", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), false},
		{"empty", nil, false},
	} {
		if got := AllowedPasteContent(tc.data); got != tc.want {
			t.Errorf("%s: AllowedPasteContent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPasteTypeAllowed(t *testing.T) {
	zip := []byte("PK\x03\x04archive")
	pdf := []byte("%PDF-1.7\n")
	if !pasteTypeAllowed(false, "images-and-pdf", zip) {
		t.Error("unwalled session should keep existing paste behaviour")
	}
	if pasteTypeAllowed(true, "images-and-pdf", zip) {
		t.Error("walled session with default setting must refuse .zip")
	}
	if pasteTypeAllowed(true, "", zip) {
		t.Error("walled session with empty setting must refuse .zip")
	}
	if !pasteTypeAllowed(true, "images-and-pdf", pdf) {
		t.Error("walled session with default setting must allow PDF")
	}
	if !pasteTypeAllowed(true, "any", zip) {
		t.Error(`walled session with paste = "any" must allow .zip`)
	}
}

func TestForwardSpecUnixSocket(t *testing.T) {
	sock := "/run/user/1000/" + brand.Name + "/p/abcd1234/3000.sock"
	got, err := forwardSpec(ForwardPair{Local: 8080, Remote: 3000, RemoteSocket: sock})
	if err != nil {
		t.Fatalf("valid socket: %v", err)
	}
	if want := "127.0.0.1:8080:" + sock; got != want {
		t.Fatalf("forwardSpec = %q, want %q", got, want)
	}
	for _, bad := range []string{"relative/3000.sock", "/tmp/a:b.sock", "/tmp/a\nb.sock", "/tmp/a b.sock"} {
		if _, err := forwardSpec(ForwardPair{Local: 8080, RemoteSocket: bad}); err == nil {
			t.Errorf("forwardSpec(%q) succeeded, want error", bad)
		}
	}
	// Verify SSH.Forward includes the StreamLocal -L flag when given a RemoteSocket.
	s := NewSSH(context.Background(), Host{Name: "box", SSH: "box"}, false)
	if _, err := s.Forward(context.Background(), []ForwardPair{{Local: 8080, RemoteSocket: "not/abs"}}); err == nil || !strings.Contains(err.Error(), "invalid host port socket") {
		t.Errorf("SSH.Forward with bad socket = %v, want bad_socket error", err)
	}
}

package supervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPasteChecksBytesBeforeGuestWrite(t *testing.T) {
	for name, data := range map[string][]byte{
		"image.png":   []byte("\x89PNG\r\n\x1a\n"),
		"image.gif":   []byte("GIF89a"),
		"file.pdf":    []byte("%PDF-1.7\n"),
		"archive.zip": []byte("PK\x03\x04zip"),
		"renamed.png": []byte("plain text despite the name"),
	} {
		t.Run(name, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), name))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(data); err != nil {
				t.Fatal(err)
			}
			err = checkPasteMedia(file, int64(len(data)))
			refused := name == "archive.zip" || name == "renamed.png"
			if (err != nil) != refused {
				t.Fatalf("media check = %v, refused=%v", err, refused)
			}
		})
	}
}

func TestUploadReceiverRejectsWrongSizeHashAndFrames(t *testing.T) {
	for _, scenario := range []string{"good", "wrong hash", "short", "extra", "wrong id", "other operation"} {
		t.Run(scenario, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			file, err := os.CreateTemp(t.TempDir(), "spool")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { local.Close(); peer.Close() })
			defer stop()
			data := []byte("abc")
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if scenario == "wrong hash" {
				digest = "wrong"
			}
			if scenario == "short" {
				data = data[:2]
			}
			if scenario == "extra" {
				data = append(data, 'd')
			}
			id := uint64(7)
			if scenario == "wrong id" {
				id++
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				writer := proto.NewWriter(peer)
				if scenario == "other operation" {
					_ = writer.Write(proto.TPing, nil)
					return
				}
				_ = writer.Write(proto.TUploadChunk, proto.EncodeChunk(id, data))
				_ = writer.WriteJSON(proto.TUploadEnd, proto.UploadEnd{ID: 7, SHA256: digest})
			}()
			_, err = receiveUpload(ctx, &client{conn: local}, bufio.NewReader(local), proto.UploadBegin{ID: 7, Size: 3}, file)
			local.Close()
			peer.Close()
			<-done
			if (err == nil) != (scenario == "good") {
				t.Fatalf("receive result = %v", err)
			}
		})
	}
}

func TestFileShutdownCancelsAndJoinsBeforeAnotherAdmission(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	s := &Supervisor{rec: record{phase: host.LifeRunning}}
	ctx, finish, err := s.beginFileTransfer(&client{conn: local})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.closeFileTransfers(); close(done) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("file shutdown did not cancel")
	}
	select {
	case <-done:
		t.Fatal("file shutdown returned before the receiver joined")
	default:
	}
	finish()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("file shutdown did not join")
	}
	if _, _, err := s.beginFileTransfer(&client{conn: local}); err == nil {
		t.Fatal("a stopped transfer owner admitted another file")
	}
}

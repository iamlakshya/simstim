package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

type memoryCABoundaryConn struct {
	net.Conn
	mu       sync.Mutex
	outbound []byte
	overflow bool
}

func (c *memoryCABoundaryConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if len(p) > (4<<20)-len(c.outbound) {
		c.overflow = true
		c.mu.Unlock()
		return 0, errors.New("memory boundary transcript exceeds its limit")
	}
	// Record the actual server write before delivery. Receipt of the complete
	// control response below proves delivery and orders the capture snapshot.
	c.outbound = append(c.outbound, p...)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

// These representations stay in memory. No test fixture writes a private key.
func memoryCABoundaryNeedles(t *testing.T, key *ecdsa.PrivateKey) [][]byte {
	t.Helper()
	ec, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal("cannot derive the in-memory EC comparison")
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal("cannot derive the in-memory PKCS8 comparison")
	}
	needles := [][]byte{[]byte("PRIVATE KEY")}
	for _, der := range [][]byte{ec, pkcs8} {
		needles = append(needles, der, []byte(base64.StdEncoding.EncodeToString(der)), []byte(base64.RawStdEncoding.EncodeToString(der)), []byte(hex.EncodeToString(der)))
	}
	needles = append(needles, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ec}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	scalar := key.D.FillBytes(make([]byte, 32))
	needles = append(needles, scalar, []byte(base64.StdEncoding.EncodeToString(scalar)), []byte(hex.EncodeToString(scalar)), []byte(key.D.String()))
	return needles
}

func TestLiveServerControlAndFilesContainNoActualInMemorySigningMaterial(t *testing.T) {
	owned := t.TempDir()
	for key, name := range map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_HOME": "data", "XDG_DATA_DIRS": "data-dirs", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run", "GNUPGHOME": "gnupg", "TMPDIR": "tmp"} {
		path := filepath.Join(owned, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	parent, child := net.Pipe()
	record := &memoryCABoundaryConn{Conn: child}
	srv, err := NewServer(record, Options{OriginRoots: x509.NewCertPool(), Dialer: &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("memory boundary permits no DNS")
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			return nil, errors.New("memory boundary permits no origin connection")
		},
	}})
	if err != nil {
		_ = parent.Close()
		_ = child.Close()
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { srv.Serve(); close(served) }()
	session := link.New(parent, link.Supervisor, LinkOptions())
	t.Cleanup(func() {
		_ = srv.Close()
		_ = session.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("owned memory boundary server did not finish")
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	control, err := session.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = control.SetDeadline(time.Now().Add(20 * time.Second))
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: api.PresetLocked}})
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(InitArgs{SessionID: "camem234", Agent: "shell", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.NewWriter(control).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlInit, Args: args}); err != nil {
		t.Fatal(err)
	}
	frame, err := proto.Read(bufio.NewReader(control))
	if err != nil || frame.Type != proto.TResponse {
		t.Fatal("live memory server produced no actual control response")
	}
	var response proto.Response
	if err := json.Unmarshal(frame.Payload, &response); err != nil || !response.OK {
		t.Fatal("live memory server rejected its init")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &fields); err != nil || len(fields) != 1 || fields["ca_cert"] == nil {
		t.Fatal("actual live init exported more than its public certificate")
	}
	needles := memoryCABoundaryNeedles(t, srv.ca.key)
	leaf, err := srv.ca.certificate("api.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leafKey, ok := leaf.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("live leaf has no ECDSA key for the memory-only comparison")
	}
	needles = append(needles, memoryCABoundaryNeedles(t, leafKey)...)
	for _, root := range []string{paths.DataDir(), paths.StateDir(), paths.RuntimeDir(), paths.SessionDir("camem234")} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "public-only.pem"), srv.ca.publicPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(data []byte) {
		t.Helper()
		for _, needle := range needles {
			if bytes.Contains(data, needle) {
				t.Fatal("actual in-memory CA or leaf private material escaped its live boundary")
			}
		}
	}
	check(frame.Payload)
	record.mu.Lock()
	wire := append([]byte(nil), record.outbound...)
	overflow := record.overflow
	record.mu.Unlock()
	if overflow || len(wire) == 0 {
		t.Fatal("actual memory boundary transcript is absent or exceeded its limit")
	}
	check(wire)
	root, err := os.OpenRoot(owned)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	count, total := 0, 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (32<<20)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		count++
		total += len(data)
		if len(data) > 32<<20 || total > 128<<20 || count > 4096 {
			return errors.New("owned memory boundary scan exceeds its limit")
		}
		check(data)
		return nil
	})
	if err != nil || count < 4 {
		t.Fatal("owned memory boundary did not scan its complete nonempty file tree")
	}
	select {
	case <-served:
		t.Fatal("memory server ended before its live scan")
	default:
	}
}

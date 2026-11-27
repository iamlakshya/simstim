package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRekey(t *testing.T) {
	ctx := context.Background()
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	oldK, newK := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(newK, "changed"))
	shows(t, dir, newK)
	if err := writeTestPin("box", []hostKey{oldK}); err != nil {
		t.Fatal(err)
	}
	pinnedFP := func() string {
		keys, err := readPin("box")
		if err != nil {
			t.Fatal(err)
		}
		return keys[0].Fingerprint()
	}
	no := errors.New("no")
	var gotOld, gotNew string
	if _, _, err := Rekey(ctx, h, "", func(o, n string) error { gotOld, gotNew = o, n; return no }); !errors.Is(err, no) || pinnedFP() != oldK.Fingerprint() {
		t.Fatalf("a no: %v, pin %s", err, pinnedFP())
	}
	if gotOld != oldK.Fingerprint() || gotNew != newK.Fingerprint() {
		t.Errorf("confirm got %s and %s", gotOld, gotNew)
	}
	for _, call := range calls(t, dir, "fetch") {
		for _, o := range append(wantFetch, "StrictHostKeyChecking=accept-new") {
			if !hasOption(call, o) {
				t.Errorf("rekey's connection lacks -o %s: %q", o, call)
			}
		}
	}
	// --host-key that the host does not show: no question.
	asked := false
	_, _, err := Rekey(ctx, h, oldK.Fingerprint(), func(string, string) error { asked = true; return nil })
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "host_key_mismatch" || asked {
		t.Errorf("a --host-key that differs: %v, asked %v", err, asked)
	}
	// The yes retires an existing master through its concrete socket.
	if controlMaster {
		args, setupErr := (&SSH{Host: h}).baseArgs()
		if setupErr != nil {
			t.Fatal(setupErr)
		}
		previousSocket := expandedTestSocket(t, args)
		if err := os.WriteFile(previousSocket, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	o, n, err := Rekey(ctx, h, newK.Fingerprint(), func(string, string) error { return nil })
	if err != nil || o == n || pinnedFP() != newK.Fingerprint() {
		t.Fatalf("a yes: %v, pin %s", err, pinnedFP())
	}
	all := sshCalls(t, dir)
	if controlMaster && !strings.Contains(all[len(all)-1], " -O exit -- placeholder") {
		t.Errorf("the master was not stopped after the rekey: %q", all[len(all)-1])
	}
	// The host shows the pinned key: nothing to ask, nothing changes.
	o, n, err = Rekey(ctx, h, "", func(string, string) error { asked = true; return nil })
	if err != nil || o != n || asked {
		t.Errorf("the same key: %s %s %v, asked %v", o, n, err, asked)
	}
	// A pin that cannot be read: rekey says so.
	if err := os.WriteFile(pinPath("box"), []byte(pinHeader("box")+"\n# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Rekey(ctx, h, "", func(o, _ string) error { gotOld = o; return no }); !errors.Is(err, no) || !strings.HasPrefix(gotOld, "the pin cannot be read: ") {
		t.Errorf("an unreadable pin: %q, %v", gotOld, err)
	}
	// A file that is not box's pin, or names no host: no question, and
	// it stays.
	for _, body := range [][]byte{pinContent("other", []hostKey{oldK}), []byte("# empty\n")} {
		if err := os.WriteFile(pinPath("box"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		asked = false
		_, _, err := Rekey(ctx, h, "", func(string, string) error { asked = true; return nil })
		if after, _ := os.ReadFile(pinPath("box")); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || asked || string(after) != string(body) {
			t.Errorf("another host's pin: %v, asked %v, the file %q", err, asked, after)
		}
	}
	if err := os.WriteFile(pinPath("box"), []byte(pinHeader("box")+"\n# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A key that the user's config revokes: no question.
	put(t, dir, "trust-log", trustLog(newK, "revoked"))
	if _, _, err := Rekey(ctx, h, "", func(string, string) error { asked = true; return nil }); !errors.As(err, &ue) || ue.Code != "host_key_revoked" || asked {
		t.Errorf("a revoked key: %v", err)
	}
	// A host with no pin yet.
	put(t, dir, "trust-log", trustLog(newK, "unknown"))
	if err := forgetPin("box"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(tiersPath())
	if o, _, err := Rekey(ctx, h, "", func(string, string) error { return nil }); err != nil || o != "" || !HasPin("box") {
		t.Errorf("no pin before: %q, %v", o, err)
	}
}

// Cancelling Rekey while fetchHostKey waits after writing the temporary key,
// or right before writePin on the --confirm path, fails with context.Canceled
// and leaves the old pin untouched on disk.
func TestRekeyStopsWhenCancelled(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	oldK, newK := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(newK, "changed"))
	shows(t, dir, newK)
	if err := writeTestPin("box", []hostKey{oldK}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pinPath("box"))
	if err != nil {
		t.Fatal(err)
	}

	// Case 1: ctx cancels right after fetchHostKey's ssh writes the temporary key.
	put(t, dir, "fetch-hold", "")
	wrote := filepath.Join(dir, "fetch-wrote")
	rctx, cancel := context.WithCancel(context.Background())
	confirmed := false
	done := make(chan error, 1)
	go func() {
		_, _, err := Rekey(rctx, h, newK.Fingerprint(), func(string, string) error {
			confirmed = true
			return nil
		})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wrote); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(wrote); err != nil {
		cancel()
		t.Fatal("fetchHostKey did not write the temporary key")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || confirmed {
		t.Fatalf("Rekey cancelled during fetch = %v, confirmed=%v", err, confirmed)
	}
	if after, _ := os.ReadFile(pinPath("box")); string(after) != string(before) {
		t.Fatalf("pin changed after cancelled fetch:\n%s", after)
	}

	// Direct fetchHostKey check: cancelling ctx after the temporary file holds
	// the key still returns context.Canceled rather than the fetched keys.
	_ = os.Remove(wrote)
	fctx, fcancel := context.WithCancel(context.Background())
	fdone := make(chan error, 1)
	go func() {
		_, err := fetchHostKey(fctx, h, false, "")
		fdone <- err
	}()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wrote); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fcancel()
	if err := <-fdone; !errors.Is(err, context.Canceled) {
		t.Fatalf("fetchHostKey cancelled after writing key = %v, want context.Canceled", err)
	}

	// Case 2: ctx cancels inside confirm when confirm returns nil (--confirm path).
	_ = os.Remove(filepath.Join(dir, "fetch-hold"))
	rctx2, cancel2 := context.WithCancel(context.Background())
	_, _, err = Rekey(rctx2, h, newK.Fingerprint(), func(string, string) error {
		cancel2()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rekey cancelled in confirm = %v, want context.Canceled", err)
	}
	if after, _ := os.ReadFile(pinPath("box")); string(after) != string(before) {
		t.Fatalf("pin changed after cancelled confirm:\n%s", after)
	}
}

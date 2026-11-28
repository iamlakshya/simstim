package api

import (
	"bytes"
	"strings"
	"testing"
)

func TestExecRecordRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	big := bytes.Repeat([]byte("a"), execChunkSize+123)
	if err := WriteExecChunk(&wire, ExecRecordStdout, big); err != nil {
		t.Fatal(err)
	}
	if err := WriteExecChunk(&wire, ExecRecordStderr, []byte("err line\n")); err != nil {
		t.Fatal(err)
	}
	if err := WriteExecExit(&wire, 42); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code, err := ReadExecStream(&wire, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 42 {
		t.Fatalf("code = %d, want 42", code)
	}
	if !bytes.Equal(out.Bytes(), big) {
		t.Fatalf("stdout len = %d, want %d", out.Len(), len(big))
	}
	if errOut.String() != "err line\n" {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestCheckKeepPath(t *testing.T) {
	for _, ok := range []string{".bash_history", ".zsh_history", ".local/share/fish/fish_history", ".psql_history"} {
		got, err := CheckKeepPath(ok)
		if err != nil || got != ok {
			t.Errorf("CheckKeepPath(%q) = %q, %v; want %q, nil", ok, got, err, ok)
		}
	}
	for _, bad := range []string{
		"", " ", ".bashrc", "./.bashrc", ".profile", ".bash_profile", ".zshrc", ".zprofile", ".zshenv",
		".config/fish/config.fish", ".config/fish/conf.d/evil.fish", ".claude/settings.json",
		"/etc/passwd", "../secret", "~/.bash_history", ".foo/", "a\nb",
	} {
		if got, err := CheckKeepPath(bad); err == nil {
			t.Errorf("CheckKeepPath(%q) succeeded with %q, want error", bad, got)
		}
	}
	// Verify error mentions startup file for .bashrc.
	if _, err := CheckKeepPath(".bashrc"); err == nil || !strings.Contains(err.Error(), ".bashrc") {
		t.Errorf("err = %v", err)
	}
}

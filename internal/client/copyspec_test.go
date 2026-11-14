package client

import "testing"

func TestCopySourceDriveAndUNCGrammar(t *testing.T) {
	for _, tc := range []struct{ raw, host, guest string }{
		{`C:\work\input.txt`, `C:\work\input.txt`, ""},
		{`C:\work\input.txt:~/input.txt`, `C:\work\input.txt`, "/home/agent/input.txt"},
		{`\\server\share\input.txt:/opt/input.txt`, `\\server\share\input.txt`, "/opt/input.txt"},
	} {
		got, err := ParseCopySource(tc.raw, `C:\Users\example`, true)
		if err != nil || got.Host != tc.host || got.Guest != tc.guest {
			t.Fatalf("%q = %+v, %v", tc.raw, got, err)
		}
	}
}

func TestCopySourceRejectsAmbiguousTargetAndRuntime(t *testing.T) {
	for _, raw := range []string{"file:", "file:relative", "file:/a:/b", "file:/proc/self/mem", "file:/x/../a", "file:/repo/.git/config"} {
		if _, err := ParseCopySource(raw, "/home/you", false); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

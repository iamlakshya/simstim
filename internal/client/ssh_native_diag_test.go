package client

import (
	"errors"
	"strings"
	"testing"
)

func TestNativeStreamingDiagnosisPreservesCanonicalPinAndFailureRules(t *testing.T) {
	alias := "fixture-box"
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	changed := "The fingerprint for the ED25519 key sent by the remote host is\n" + fingerprint + ".\n" +
		"Host key for " + alias + " has changed and you have requested strict checking.\n"
	unknown := "No ED25519 host key is known for " + alias + " and you have requested strict checking.\n"
	stop := "Host key verification failed.\n"
	for name, log := range map[string]string{
		"changed":                changed + stop,
		"unknown":                unknown + stop,
		"CRLF":                   strings.ReplaceAll(changed+stop, "\n", "\r\n"),
		"case folded alias":      strings.ReplaceAll(changed, alias, strings.ToUpper(alias)) + stop,
		"empty trailing lines":   changed + stop + " \r\n\n",
		"ambiguous shown keys":   changed + changed + stop,
		"ambiguous unknown keys": unknown + unknown + stop,
		"another changed alias":  changed + strings.ReplaceAll(changed, alias, "other") + stop,
		"another unknown alias":  strings.ReplaceAll(unknown, alias, "other") + stop,
		"no final stop":          changed,
		"later native text":      changed + stop + "debug1: later text\n",
		"service accepted":       changed + "debug1: SSH2_MSG_SERVICE_ACCEPT received\n" + stop,
		"auth rejected":          changed + "Permission denied (publickey).\n" + stop,
		"authenticated":          changed + "Authenticated to fixture using none.\n" + stop,
		"failure precedence":     "ssh: connect to host fixture: Connection refused\nssh: Could not resolve hostname fixture: unknown\n",
		"large intact prefix":    strings.Repeat("debug2: bounded native detail\n", 30_000) + changed + stop,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := scanNativeSSHDiagnosis(strings.NewReader(log), alias)
			if err != nil {
				t.Fatal(err)
			}
			shownType, shown, stopped := parseHostKeyStop(log, alias)
			if got.stopped != stopped || stopped && (got.shownType != shownType || got.shown != shown) {
				t.Fatalf("stream pin facts=%+v, canonical=(%q,%q,%v)", got, shownType, shown, stopped)
			}
			expected := ""
			for _, failure := range sshFailures {
				if failure.re.MatchString(log) {
					expected = failure.says
					break
				}
			}
			if got.failReason != expected {
				t.Fatalf("stream reason=%q, canonical=%q", got.failReason, expected)
			}
		})
	}
}

type failingNativeDiagnosticReader struct{ err error }

func (r failingNativeDiagnosticReader) Read([]byte) (int, error) { return 0, r.err }

func TestNativeDiagnosisNeverClassifiesAnIncompleteRead(t *testing.T) {
	cause := errors.New("owned snapshot read failed")
	if _, err := scanNativeSSHDiagnosis(failingNativeDiagnosticReader{err: cause}, "box"); !errors.Is(err, cause) {
		t.Fatalf("snapshot read error was lost: %v", err)
	}
	if _, err := scanNativeSSHDiagnosis(strings.NewReader(strings.Repeat("x", agentNativeLogLine+1)), "box"); err == nil {
		t.Fatal("over-bound line was silently classified as a complete transcript")
	}
}

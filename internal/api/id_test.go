package api

import (
	"strings"
	"testing"
)

// A word of 8 characters of the ID's alphabet, with a digit, looks like an
// ID, and no name may have that form. A word of 8 letters, such as
// "frontend", is a name. A name may not be longer than SessionNameMax
// characters either.
func TestNamesAndIDs(t *testing.T) {
	for word, want := range map[string]bool{
		"abcd2345": true, "zzzzzzz7": true, "a2b3c4d5": true, "22222222": true,
		"frontend": false, "zzzzzzzz": false, "abcd234": false, "abcd23456": false, "abcd2341": false, "abcd2348": false,
		"Abcd2345": false, "abcd-345": false, "": false,
	} {
		if LooksLikeID(word) != want {
			t.Errorf("LooksLikeID(%q) = %v", word, !want)
		}
	}
	for name, ok := range map[string]bool{
		"abcd2345": false, "\tabcd2345\n": false, strings.Repeat("é", SessionNameMax+1): false,
		"abcd-2345": true, "auth-fix": true, "frontend": true, "database": true, strings.Repeat("é", SessionNameMax): true, "": true,
	} {
		msg, hint, got := CheckName(name)
		if got != ok || !ok && (msg == "" || hint == "") {
			t.Errorf("CheckName(%.12q) = %q, %q, %v", name, msg, hint, got)
		}
	}
	if _, hint, _ := CheckName("abcd2345"); hint != "Add a dash, as in abcd-2345, or use a name of another length." {
		t.Errorf("the hint: %q", hint)
	}
}

// Every generated session ID looks like an ID and is refused as a session
// name, so no new ID can equal a valid session name.
func TestNewSessionIDNeverPassesCheckName(t *testing.T) {
	for range 20000 {
		id := NewSessionID()
		if !LooksLikeID(id) {
			t.Fatalf("NewSessionID() = %q, want LooksLikeID", id)
		}
		if _, _, ok := CheckName(id); ok {
			t.Fatalf("NewSessionID() = %q passed CheckName", id)
		}
	}
}

package api

import (
	"crypto/rand"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SessionIDDigits are the digits of SessionIDAlphabet. The ID that the
// helper makes holds at least one of them.
const SessionIDDigits = "234567"

// SessionIDLength is the length of the ID that the helper makes for a
// session.
const SessionIDLength = 8

// SessionNameMax is the most characters that a name a user gives a
// session may have.
const SessionNameMax = 64

// NewSessionID makes a random session ID: 8 characters of
// SessionIDAlphabet, at least one of them a digit. A name without a digit
// then never looks like an ID (cli-spec 9.2). An ID drawn without one gets
// a random digit at a random place. The helper and the launcher's demo
// make their IDs here.
func NewSessionID() string {
	var b [SessionIDLength + 2]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, SessionIDLength)
	for i := range out {
		out[i] = SessionIDAlphabet[int(b[i])%len(SessionIDAlphabet)]
	}
	if !strings.ContainsAny(string(out), SessionIDDigits) {
		out[int(b[SessionIDLength])%len(out)] = SessionIDDigits[int(b[SessionIDLength+1])%len(SessionIDDigits)]
	}
	return string(out)
}

// LooksLikeID reports whether word has the form of the ID that the helper
// makes for a session: 8 characters of SessionIDAlphabet, at least one of
// them a digit. Such a word is always an ID and never a name, so that an
// address with it means one session (docs/contributing/cli-spec.md 9.2).
// No name may have that form. A word of 8 letters, such as "frontend", is
// a name: no new ID is all letters.
func LooksLikeID(word string) bool {
	if len(word) != SessionIDLength || !strings.ContainsAny(word, SessionIDDigits) {
		return false
	}
	for _, c := range word {
		if !strings.ContainsRune(SessionIDAlphabet, c) {
			return false
		}
	}
	return true
}

// CheckName checks name, a name that a user gives a session with new or
// rename, without the spaces at its ends. A name that looks like an ID, or
// that has more than SessionNameMax characters, cannot be one: ok is then
// false, with the message and the hint of the error. The client and the
// helper both refuse such a name with them, and the code bad_name.
func CheckName(name string) (msg, hint string, ok bool) {
	name = strings.TrimSpace(name)
	switch {
	case LooksLikeID(name):
		return "a name of 8 letters a to z and digits 2 to 7, with a digit, looks like a session ID",
			"Add a dash, as in " + name[:SessionIDLength/2] + "-" + name[SessionIDLength/2:] + ", or use a name of another length.", false
	case utf8.RuneCountInString(name) > SessionNameMax:
		return "a session name can have at most " + strconv.Itoa(SessionNameMax) + " characters",
			"Use a shorter name.", false
	}
	return "", "", true
}

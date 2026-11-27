package secretbind

import (
	"context"
	"strings"
	"testing"
)

func TestFormatStreamAllBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		kind     string
		username string
	}{
		{"raw", "{value}", "raw", ""},
		{"bearer", "Bearer {value}", "bearer", ""},
		{"basic", "basic:account:{value}", "basic", "account"},
		{"empty Basic user remains valid", "basic::{value}", "basic", ""},
		{"Basic user spaces remain valid", "basic: account :{value}", "basic", " account "},
		{"Basic unicode user", "basic:\u00e9\u6f22\U0001f600:{value}", "basic", "\u00e9\u6f22\U0001f600"},
		{"partial placeholder is metadata", "basic:{valu:{value}", "basic", "{valu"},
		{"empty", "", "", ""},
		{"lowercase Bearer", "bearer {value}", "", ""},
		{"wrong Basic case", "Basic:account:{value}", "", ""},
		{"bare placeholder prefix", "{value", "", ""},
		{"raw trailing byte", "{value}x", "", ""},
		{"Bearer extra space", "Bearer  {value}", "", ""},
		{"Basic suffix missing", "basic:account", "", ""},
		{"Basic suffix trailing byte", "basic:account:{value}x", "", ""},
		{"Basic colon", "basic:account:part:{value}", "", ""},
		{"Basic placeholder", "basic:a{value}b:{value}", "", ""},
		{"overlapping placeholder", "basic:{{value}:{value}", "", ""},
		{"Basic newline", "basic:account\n:{value}", "", ""},
		{"Basic nul", "basic:account\x00:{value}", "", ""},
		{"Basic DEL", "basic:account\x7f:{value}", "", ""},
		{"Basic C1", "basic:account\u0085:{value}", "", ""},
		{"Basic format character", "basic:account\u200b:{value}", "", ""},
		{"Basic bidi control", "basic:account\u202e:{value}", "", ""},
		{"Basic malformed UTF8", "basic:account\xff:{value}", "", ""},
		{"Basic truncated UTF8", "basic:account\xc3:{value}", "", ""},
		{"Basic overlong UTF8", "basic:account\xc0\xaf:{value}", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, username, materialErr := ParseFormat(tc.raw)
			if (materialErr == nil) != (tc.kind != "") || kind != tc.kind || username != tc.username {
				t.Fatal("materialised format differs from the labelled grammar")
			}
			for split := 0; split <= len(tc.raw); split++ {
				validator := NewFormatValidator(context.Background())
				err := finishScalarParts(validator, tc.raw[:split], tc.raw[split:])
				if (err == nil) != (tc.kind != "") {
					t.Fatalf("format acceptance differs at byte boundary %d", split)
				}
			}
		})
	}
}

func TestFormatStreamByteChunksAndLongUser(t *testing.T) {
	valid := NewFormatValidator(context.Background())
	if _, err := valid.Write([]byte("basic:")); err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("account", 1024))
	for range 1024 {
		if n, err := valid.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("unselected Basic user acquired a cap: count=%d error=%v", n, err)
		}
	}
	for _, b := range []byte("\u00e9\u6f22\U0001f600:{value}") {
		if _, err := valid.Write([]byte{b}); err != nil {
			t.Fatalf("split UTF8 or suffix was rejected: %v", err)
		}
	}
	if err := valid.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestFormatStreamPlaceholderAcrossManyChunks(t *testing.T) {
	validator := NewFormatValidator(context.Background())
	raw := "basic:account-{value}-extra:{value}"
	var err error
	for _, b := range []byte(raw) {
		_, err = validator.Write([]byte{b})
		if err != nil {
			break
		}
	}
	if err == nil {
		err = validator.Finish()
	}
	if err == nil {
		t.Fatal("username placeholder split across chunks was accepted")
	}
}

func TestFormatStreamRejectsUnicodeControlAndFormatClasses(t *testing.T) {
	// Cover every C0/C1 control and representatives of several Cf ranges, with
	// byte boundaries tested separately above. Ordinary Unicode stays valid.
	for r := rune(0); r <= 0x9f; r++ {
		if r >= 0x20 && r < 0x7f {
			continue
		}
		if _, _, err := ParseFormat("basic:a" + string(r) + "b:{value}"); err == nil {
			t.Fatalf("Unicode control U+%04X was accepted", r)
		}
	}
	for _, r := range []rune{0x00ad, 0x061c, 0x180e, 0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x202a, 0x202c, 0x2060, 0x2066, 0xfeff, 0xfff9, 0xe0001} {
		if _, _, err := ParseFormat("basic:a" + string(r) + "b:{value}"); err == nil {
			t.Fatalf("Unicode format character U+%04X was accepted", r)
		}
	}
}

package proc

import (
	"strings"
	"testing"
)

func TestLinuxBirthTicksUsesTheKernelFieldAfterTheLastCommandBoundary(t *testing.T) {
	raw := "22 (name ) with spaces)) S " + strings.Repeat("0 ", 18) + "12345 0 0\n"
	got, err := LinuxBirthTicksV1([]byte(raw))
	if err != nil || got != "12345" {
		t.Fatalf("birth=%q err=%v", got, err)
	}
	for _, raw := range []string{"22 broken", "22 (name) S 0", "22 (name) S " + strings.Repeat("0 ", 18) + "bad", "22 (name) S " + strings.Repeat("0 ", 19)} {
		if _, err := LinuxBirthTicksV1([]byte(raw)); err == nil {
			t.Fatal("an uncertain birth was accepted")
		}
	}
}

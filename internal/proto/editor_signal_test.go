package proto

import "testing"

func TestEditorSignalsUseGuestNumbersOnEveryLaptopPlatform(t *testing.T) {
	for name, want := range map[string]uint32{"HUP": 1, "INT": 2, "QUIT": 3, "ILL": 4, "ABRT": 6, "FPE": 8, "KILL": 9, "USR1": 10, "SEGV": 11, "USR2": 12, "PIPE": 13, "ALRM": 14, "TERM": 15} {
		if got, ok := EditorSignalNumberV1(name); !ok || got != want {
			t.Fatal("SSH signal became a laptop-native or unknown guest number", name, got, ok)
		}
	}
	for _, name := range []string{"SIGUSR1", "30", "31", "TERM;command", "", "WINCH"} {
		if number, ok := EditorSignalNumberV1(name); ok || number != 0 {
			t.Fatal("unsupported signal became a guest operation", name, number)
		}
	}
}

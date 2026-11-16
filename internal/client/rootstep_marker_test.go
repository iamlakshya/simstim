package client

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRootMarkerForwardsExactBytesAndNeedsACompleteBoundedLine(t *testing.T) {
	for _, tt := range []struct {
		name     string
		chunks   []string
		complete bool
	}{
		{"line", []string{"__ROOT_STEP_DONE__\n"}, true},
		{"terminalCRLF", []string{"__ROOT_STEP_DONE__\r\n"}, true},
		{"split", []string{"__ROOT_", "STEP_DONE__", "\n"}, true},
		{"prefix", []string{"prefix __ROOT_STEP_DONE__\n"}, false},
		{"unterminated", []string{"__ROOT_STEP_DONE__"}, false},
		{"overlong", []string{strings.Repeat("x", 65), "__ROOT_STEP_DONE__\n"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := &rootMarkerWriter{out: &output}
			for _, chunk := range tt.chunks {
				if _, err := writer.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if output.String() != strings.Join(tt.chunks, "") || writer.done() != tt.complete {
				t.Fatalf("output=%q complete=%t", output.String(), writer.done())
			}
		})
	}
}

type invalidRootOutput struct{}

func (invalidRootOutput) Write(p []byte) (int, error) { return len(p) + 1, nil }

func TestRootMarkerRejectsAnInvalidOutputCountWithoutCompletion(t *testing.T) {
	writer := &rootMarkerWriter{out: invalidRootOutput{}}
	if _, err := writer.Write([]byte("__ROOT_STEP_DONE__\n")); !errors.Is(err, io.ErrShortWrite) || writer.done() {
		t.Fatalf("invalid output became completion: err=%v done=%t", err, writer.done())
	}
}

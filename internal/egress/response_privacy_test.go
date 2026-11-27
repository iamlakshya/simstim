package egress

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestResponseDerivedValuesRedactEveryFormattingVerbAndValueForm(t *testing.T) {
	const marker = "derived-response-marker-never-print"
	pair := responseEchoPair{Value: marker, Sentinel: api.Secret(strings.Repeat("z", len(marker)))}
	snapshot := responseTestEchoes(t, pair)
	match := responseEchoMatch{length: len(marker), sentinel: marker}
	reader, err := newResponseCredentialReader(t.Context(), io.NopCloser(strings.NewReader(marker)), func(context.Context) (*responseEchoSnapshot, error) { return snapshot, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.buffer = []byte(marker)
	reader.replacement = marker
	for _, value := range []any{pair, &pair, *snapshot, snapshot, match, &match, reader, *reader} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			text := fmt.Sprintf(verb, value)
			if text != api.Redacted || strings.Contains(text, marker) {
				t.Fatal("derived response value bypassed its explicit redacting Formatter")
			}
		}
	}
}

type responsePrivacySource struct {
	started, closed chan struct{}
	once            sync.Once
}

func (s *responsePrivacySource) Close() error { s.once.Do(func() { close(s.closed) }); return nil }
func (s *responsePrivacySource) Read(p []byte) (int, error) {
	close(s.started)
	for {
		select {
		case <-s.closed:
			return 0, io.ErrClosedPipe
		default:
		}
		for i := range p {
			p[i] = "format-marker"[i%len("format-marker")]
		}
		runtime.Gosched()
	}
}

func TestResponseFacadeFormattingCanOverlapAnOwnedReadAndClose(t *testing.T) {
	source := &responsePrivacySource{started: make(chan struct{}), closed: make(chan struct{})}
	reader, err := newResponseCredentialReader(t.Context(), source, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readDone := make(chan struct{})
	go func() { defer close(readDone); p := make([]byte, 1); _, _ = reader.Read(p) }()
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("owned formatting-oracle read never began")
	}
	formatStarted := make(chan struct{})
	formatDone := make(chan bool, 1)
	go func() {
		good := true
		for round := range 256 {
			for _, value := range []any{reader, *reader} {
				for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
					good = good && fmt.Sprintf(verb, value) == api.Redacted
				}
			}
			if round == 0 {
				close(formatStarted)
			}
		}
		formatDone <- good
	}()
	closeDone := make(chan struct{})
	go func() { defer close(closeDone); <-formatStarted; _ = reader.Close() }()
	select {
	case good := <-formatDone:
		if !good {
			t.Fatal("concurrent facade formatting exposed state")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("formatting did not finish")
	}
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent owned close did not finish")
	}
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("owned formatting read outlived close")
	}
}

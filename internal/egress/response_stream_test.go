package egress

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestResponseRegistrationCommitsBeforeAnySentinelEmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input, writer := io.Pipe()
	var current atomic.Pointer[responseEchoSnapshot]
	current.Store(responseTestEchoes(t))
	entered, commit := make(chan struct{}), make(chan struct{})
	reader, err := newResponseCredentialReader(ctx, input, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, responseTestRules(t), func(ctx context.Context, value api.Secret, _ string) (api.Secret, error) {
		close(entered)
		select {
		case <-commit:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		snapshot, err := newResponseEchoSnapshot([]responseEchoPair{{Value: value, Sentinel: "tok_ZZZZZZ"}})
		if err != nil {
			return "", err
		}
		current.Store(snapshot)
		return "tok_ZZZZZZ", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(writer, "tok_ABCDEF\n"); written <- err }()
	got := make(chan []byte, 1)
	failed := make(chan error, 1)
	go func() {
		p := make([]byte, 10)
		n, err := io.ReadFull(reader, p)
		if err != nil {
			failed <- err
			return
		}
		got <- p[:n]
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate did not enter the main registration gate")
	}
	select {
	case <-got:
		t.Fatal("sentinel bytes preceded registry publication")
	case err := <-failed:
		t.Fatal(err)
	default:
	}
	close(commit)
	select {
	case data := <-got:
		if string(data) != "tok_ZZZZZZ" {
			t.Fatal("committed sentinel bytes changed")
		}
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("committed sentinel was not emitted")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
}

func TestResponseCancellationInterruptsOwnedReadAndRegistryGate(t *testing.T) {
	for _, blocked := range []string{"read", "registration"} {
		t.Run(blocked, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input, writer := io.Pipe()
			defer writer.Close()
			readEntered := make(chan struct{})
			source := &responseReadEntry{ReadCloser: input, entered: readEntered}
			entered := make(chan struct{})
			reader, err := newResponseCredentialReader(ctx, source, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, responseTestRules(t), func(ctx context.Context, _ api.Secret, _ string) (api.Secret, error) {
				close(entered)
				<-ctx.Done()
				return "", ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			readDone := make(chan error, 1)
			go func() { p := make([]byte, 32); _, err := reader.Read(p); readDone <- err }()
			var writeDone chan error
			if blocked == "registration" {
				writeDone = make(chan error, 1)
				go func() { _, err := io.WriteString(writer, "tok_ABCDEF\n"); writeDone <- err }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("registration never blocked")
				}
			} else {
				select {
				case <-readEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("owned source read never began")
				}
			}
			cancel()
			select {
			case err := <-readDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled response returned a successful or unrelated result")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owned response read outlived cancellation")
			}
			if writeDone != nil {
				select {
				case <-writeDone:
				case <-time.After(5 * time.Second):
					t.Fatal("owned fixture writer remained blocked")
				}
			}
		})
	}
}

type responseReadEntry struct {
	io.ReadCloser
	entered chan struct{}
	once    atomic.Bool
}

func (r *responseReadEntry) Read(p []byte) (int, error) {
	if r.once.CompareAndSwap(false, true) {
		close(r.entered)
	}
	return r.ReadCloser.Read(p)
}

type responseCloseError struct{ io.Reader }

func (responseCloseError) Close() error { return errors.New("sensitive close detail") }

func TestResponseCloseVerificationErrorCannotBecomeCleanEOF(t *testing.T) {
	reader, err := newResponseCredentialReader(t.Context(), responseCloseError{Reader: strings.NewReader("ordinary")}, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, readErr := io.ReadAll(reader)
	if readErr == nil || strings.Contains(readErr.Error(), "sensitive") {
		t.Fatal("owned input close failure became a clean EOF or leaked its details")
	}
}

func TestResponseSSEDoesNotWaitForMaximumCarryOrOriginEOF(t *testing.T) {
	input, writer := io.Pipe()
	defer writer.Close()
	snapshot := responseTestEchoes(t, responseEchoPair{Value: api.Secret("different-" + strings.Repeat("x", 8192)), Sentinel: api.Secret("different-" + strings.Repeat("y", 8192))})
	reader, err := newResponseCredentialReader(t.Context(), input, func(context.Context) (*responseEchoSnapshot, error) { return snapshot, nil }, responseTestRules(t), func(context.Context, api.Secret, string) (api.Secret, error) {
		return "", errors.New("unexpected candidate")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	const event = "event: token\ndata: first\n\n"
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(writer, event); written <- err }()
	read := make(chan error, 1)
	go func() {
		p := make([]byte, len(event))
		_, err := io.ReadFull(reader, p)
		if err == nil && string(p) != event {
			err = errors.New("SSE bytes changed")
		}
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short SSE event waited for carry capacity or origin EOF")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

type responseObservedReader struct {
	remaining int
	reads     int
	bytes     int
}

func (r *responseObservedReader) Close() error { return nil }
func (r *responseObservedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range n {
		p[i] = 'a'
	}
	r.remaining -= n
	r.reads++
	r.bytes += n
	return n, nil
}

func TestResponseReaderDoesNotReadAheadOfItsBoundedDemand(t *testing.T) {
	source := &responseObservedReader{remaining: 64 << 20}
	reader, err := newResponseCredentialReader(t.Context(), source, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	p := make([]byte, 1)
	if n, err := reader.Read(p); err != nil || n != 1 || p[0] != 'a' {
		t.Fatal("first response byte failed")
	}
	if source.reads != 1 || source.bytes > responseReadBuffer {
		t.Fatal("response reader buffered the body ahead of demand")
	}
	if n, err := reader.Read(p); err != nil || n != 1 || source.reads != 1 {
		t.Fatal("buffered demand caused another origin read")
	}
}

func TestResponseFiniteRulesRejectUnboundedOrUncapturedGrammars(t *testing.T) {
	for _, expression := range []string{`(tok_[A-Z]+)`, `(tok_[A-Z]{1000})`, `tok_[A-Z]{6}`, `prefix(tok_[A-Z]{6})`, `(a?)`, `(?i:(key))`} {
		if _, err := newResponseRuleSnapshot([]responseMintRule{{ID: "invalid-fixture", Expression: expression}}); err == nil {
			t.Fatal("unsupported credential grammar entered the finite response helper")
		}
	}
}

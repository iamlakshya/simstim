package client

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

type agentNativeLogSummary struct {
	// Text is a full small capture or a static display message.
	Text string
	// Bytes counts the complete prefix consumed through the streaming reader.
	Bytes int64
	// Complete requires a successful intact scan through EOF.
	Complete bool
	// Classified requires a nonnil caller classifier that completed at EOF.
	Classified bool
}

// Finish returns bounded diagnostics after the owned process ended.
// Call FinishScan when the complete transcript needs semantic classification.
func (l *agentNativeLog) Finish() (string, error) {
	summary, err := l.FinishScan(nil)
	return summary.Text, err
}

// FinishScan streams the complete capture through its caller's classifier.
// Valid large captures produce static display text, not a capture failure.
func (l *agentNativeLog) FinishScan(scan func(io.Reader) error) (agentNativeLogSummary, error) {
	if l == nil {
		return agentNativeLogSummary{}, nil
	}
	l.finishOnce.Do(func() { l.finishResult, l.finishErr = l.finish(scan) })
	return l.finishResult, l.finishErr
}

func (l *agentNativeLog) finish(scan func(io.Reader) error) (agentNativeLogSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*sshWaitDelay)
	defer cancel()
	l.releaseStartupWriter()
	select {
	case <-l.done:
	case <-time.After(sshWaitDelay):
		l.fail(agentNativeLogError("the native log writer did not finish"))
	}
	type diagnostic struct {
		summary agentNativeLogSummary
		err     error
	}
	result := make(chan diagnostic, 1)
	go func() {
		summary, err := l.finalDiagnostic(ctx, scan)
		result <- diagnostic{summary, err}
	}()
	var summary agentNativeLogSummary
	var err error
	select {
	case value := <-result:
		summary, err = value.summary, value.err
	case <-ctx.Done():
		err = agentNativeLogError("the final native log scan did not finish")
		l.recordFailure(err, true)
	}
	closeError := l.closeContext(ctx)
	if closeError != nil {
		summary.Text, summary.Complete = "", false
		err = closeError
	}
	return summary, err
}

func (l *agentNativeLog) finalDiagnostic(ctx context.Context, scan func(io.Reader) error) (agentNativeLogSummary, error) {
	var summary agentNativeLogSummary
	err := l.ScanSnapshot(ctx, func(reader io.Reader) error {
		display := &agentNativeDisplayReader{reader: reader, text: make([]byte, 0, agentNativeLogDisplayLimit)}
		if scan == nil {
			if _, err := io.Copy(io.Discard, display); err != nil {
				return err
			}
		} else {
			if err := scan(display); err != nil {
				return err
			}
			if !display.eof {
				return agentNativeLogError("the final verifier did not scan the complete prefix")
			}
			summary.Classified = true
		}
		summary.Bytes, summary.Complete = display.count, display.eof
		if display.large {
			summary.Text = "SSH diagnostic log exceeds the display limit"
		} else {
			summary.Text = string(display.text)
		}
		return nil
	})
	return summary, err
}

type agentNativeDisplayReader struct {
	reader io.Reader
	text   []byte
	count  int64
	large  bool
	eof    bool
}

func (r *agentNativeDisplayReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	if !r.large {
		if r.count > agentNativeLogDisplayLimit {
			r.large, r.text = true, nil
		} else {
			r.text = append(r.text, p[:n]...)
		}
	}
	if err == io.EOF {
		r.eof = true
	}
	return n, err //nolint:wrapcheck // io.Reader must preserve EOF for the caller's classifier.
}

// Close interrupts owned input before joining the pump. Owner callbacks
// must never call it: they cancel only the owned process and never join.
func (l *agentNativeLog) Close() error {
	if l == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshWaitDelay)
	defer cancel()
	return l.closeContext(ctx)
}

func (l *agentNativeLog) closeContext(ctx context.Context) error {
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		close(l.stop)
		l.control.Lock()
		if l.parentStop != nil {
			l.parentStop()
		}
		scan := l.scan
		l.scan = nil
		l.control.Unlock()
		l.interruptInput()
		if scan != nil {
			go func() { _ = scan.Close() }()
		}
		go func() {
			defer close(l.cleanupDone)
			<-l.done
			<-l.inputDone
			<-l.dummyDone
			err := l.cleanupFiles()
			l.control.Lock()
			l.closeErr = err
			l.control.Unlock()
		}()
	})
	select {
	case <-l.cleanupDone:
	case <-ctx.Done():
		l.recordFailure(agentNativeLogError("the native log cleanup did not finish"), true)
	}
	if failure := l.Failure(); failure != nil {
		return failure
	}
	l.control.Lock()
	err := l.closeErr
	l.control.Unlock()
	return err
}

type agentNativeOwnedPath struct {
	name, witness string
	info          os.FileInfo
	kind          os.FileMode
	held          *os.File
}

func (l *agentNativeLog) cleanupFiles() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mode != nil {
		defer l.mode.releaseNativeLog(l)
	}
	var cleanupError error
	if l.root != nil {
		for _, file := range []agentNativeOwnedPath{
			{"transcript.log", ".regular-owned", l.regularInfo, 0, l.regular},
			{"native.fifo", ".fifo-owned", l.fifoInfo, os.ModeNamedPipe, l.fifo},
		} {
			if err := l.removeOwnedPath(file); err != nil {
				cleanupError = err
			}
		}
	}
	if l.regular != nil {
		_ = l.regular.Close()
	}
	if l.ownsDirectory {
		if err := l.directory.close(); err != nil {
			cleanupError = err
		}
	}

	return cleanupError
}

func (l *agentNativeLog) removeOwnedPath(file agentNativeOwnedPath) error {
	if file.info == nil {
		return nil
	}
	retained, retainedErr := l.root.Lstat(file.witness)
	witnessOwned := retainedErr == nil && agentNativeOwnedInfo(retained, file.kind) && os.SameFile(retained, file.info)
	heldOwned := false
	if file.held != nil {
		opened, err := file.held.Stat()
		heldOwned = err == nil && agentNativeOwnedInfo(opened, file.kind) && os.SameFile(opened, file.info)
	}
	current, err := l.root.Lstat(file.name)
	if err == nil && (witnessOwned || heldOwned) && agentNativeOwnedInfo(current, file.kind) && os.SameFile(current, file.info) {
		if err := l.root.Remove(file.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return agentNativeLogError("a private capture path could not be removed")
		}
	}
	if witnessOwned {
		if err := l.root.Remove(file.witness); err != nil && !errors.Is(err, os.ErrNotExist) {
			return agentNativeLogError("a private capture witness could not be removed")
		}
	}
	return nil
}

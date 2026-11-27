package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Relay carries one owned agent connection, with two fixed buffers. EOF
// half-closes the opposite writer and lets the final response finish. A
// cancellation interrupts reads before any bounded stream close can wait.
func Relay(ctx context.Context, a, b net.Conn) error {
	stop := context.AfterFunc(ctx, func() {
		_ = a.SetDeadline(time.Now())
		_ = b.SetDeadline(time.Now())
	})
	defer stop()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 2)
	go func() { done <- copyHalf(a, b) }()
	go func() { done <- copyHalf(b, a) }()
	first := <-done
	if first != nil {
		_ = a.SetDeadline(time.Now())
		_ = b.SetDeadline(time.Now())
	}
	second := <-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(first, second)
}

func copyHalf(dst net.Conn, src net.Conn) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if err := writeBytes(dst, buffer[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				if err := half.CloseWrite(); err != nil {
					return fmt.Errorf("half-close agent stream: %w", err)
				}
				return nil
			}
			return fmt.Errorf("half-close agent stream: %w", net.ErrClosed)
		}
		if err != nil {
			return fmt.Errorf("read agent stream: %w", err)
		}
	}
}

func writeBytes(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return fmt.Errorf("write agent stream: %w", err)
		}
		if n <= 0 || n > len(p) {
			return fmt.Errorf("write agent stream: %w", io.ErrShortWrite)
		}
		p = p[n:]
	}
	return nil
}

package link

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// RelayDuplex copies opaque bytes both ways and joins both readers. A clean
// half-close preserves the reply direction. Cancellation revokes both ends.
func RelayDuplex(ctx context.Context, left, right net.Conn) error {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = left.SetDeadline(time.Now())
			_ = right.SetDeadline(time.Now())
			_ = left.Close()
			_ = right.Close()
		})
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	pump := func(destination, source net.Conn, result chan<- error) {
		_, err := io.Copy(destination, source)
		if err == nil {
			if half, ok := destination.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			}
		}
		if err != nil {
			closeBoth()
		}
		result <- err
	}
	result := make(chan error, 2)
	go pump(left, right, result)
	go pump(right, left, result)
	first, second := <-result, <-result
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(first, second)
}

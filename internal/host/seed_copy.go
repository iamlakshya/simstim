package host

import (
	"context"
	"io"
)

// seedCopy bounds cancellation latency between regular-file reads. It uses
// the same operation context that StopGitOps cancels for a seed.
func seedCopy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			written, err := dst.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

//go:build !unix

package diag

// sysMono is 0 where the system clock is not read: Now then counts from the
// start of the process, which is enough for the client's own records.
func sysMono() int64 { return 0 }

package host

import "io"

// OpenLog uses the holder's bounded log writer and rotation policy.
func OpenLog(path string) (io.WriteCloser, error) { return openHolderLog(path, holderLogMax) }

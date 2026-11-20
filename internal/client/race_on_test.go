//go:build race

package client

import "time"

// stressLimit is how long a stress test may take. The race detector makes
// code several times slower, and on a busy machine a test that needs 2 s
// of CPU can take 10 s. A cost that grows with the square of the input
// takes minutes.
const stressLimit = 30 * time.Second

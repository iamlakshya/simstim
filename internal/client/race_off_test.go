//go:build !race

package client

import "time"

// stressLimit is how long a stress test may take.
const stressLimit = time.Second

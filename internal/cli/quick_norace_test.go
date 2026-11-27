//go:build !race

package cli

import "time"

// quick is the time that a check of a hostile file may take, in the probes
// of TestConfigCheckStaysQuick and TestConfigCheckStaysSmall.
const quick = time.Second

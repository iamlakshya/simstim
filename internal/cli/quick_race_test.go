//go:build race

package cli

import "time"

// quick is the time that a check of a hostile file may take under the race
// detector, which slows the TOML package's decode several times over. A
// file that the limits fail to stop takes far longer.
const quick = 10 * time.Second

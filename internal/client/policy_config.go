package client

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

// RepoSettings is a trusted repository section. Include retains the earlier
// seed-file spelling; sandbox.include is the common layer form.
type RepoSettings struct {
	api.LayerValues
	// Include lists source files to seed (prefer sandbox.include).
	Include []string `toml:"include,omitempty"`
}

// ParseWallFloor reads a saved floor, with a user-facing error for a bad value.
func ParseWallFloor(value string) (wall.Level, error) {
	level, err := wall.ParseLevel(value)
	if err != nil {
		return wall.None, &UserError{Msg: fmt.Sprintf("%q is not a sandbox wall level", CleanLine(value)),
			Hint: "Use none, process, gvisor or microvm.", Code: "bad_floor", Kind: KindUsage}
	}
	return level, nil
}

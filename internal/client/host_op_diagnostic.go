package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// A diagnostic is display text, never ownership or deletion authority. Read
// only a bounded prefix of a native-validated regular single-link descriptor.
// In particular, never follow an ambient .who link into another user file.
func busyHostOperation(name string, owned *hostOperationFile) error {
	who := "another command changes it"
	if owned.check(context.Background()) == nil {
		file, err := cachefs.OpenFile(owned.root, owned.name+".who", os.O_RDONLY, 0600)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(file, 1024))
			_ = file.Close()
			if readErr == nil {
				if kind := CleanDetail(strings.TrimSpace(string(data))); kind != "" {
					who = kind + " changes it"
				}
			}
		}
	}
	return &UserError{Msg: fmt.Sprintf("%s is busy: %s", name, who), Hint: "Wait until it ends, then run the command again.", Code: "host_busy"}
}

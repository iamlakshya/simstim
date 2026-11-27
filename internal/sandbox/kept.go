package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ValidateKeepPath uses the same state-plan rules as restoration and capture.
// A shell startup file cannot enter through another layer or lock.
func ValidateKeepPath(name string) error {
	if err := (files.StatePlan{Files: []string{name}}).Validate(); err != nil {
		return &proto.Error{Code: "keep_refused", Message: "sandbox.keep contains an unsafe retained file", Hint: "Keep ordinary home files such as .bash_history; omit shell profiles and startup scripts."}
	}
	return nil
}

// KeptFiles appends exact retained files to an owned copy of the existing plan.
// It never makes a missing retained file into a directory.
func KeptFiles(base files.StatePlan, keep []string) (files.StatePlan, error) {
	plan := files.StatePlan{Dirs: slices.Clone(base.Dirs), Files: slices.Clone(base.Files)}
	for _, name := range keep {
		if err := ValidateKeepPath(name); err != nil {
			return files.StatePlan{}, err
		}
		if !slices.Contains(plan.Files, name) {
			plan.Files = append(plan.Files, name)
		}
	}
	if err := plan.Validate(); err != nil {
		return files.StatePlan{}, &proto.Error{Code: "keep_refused", Message: "sandbox.keep overlaps another state path", Hint: "Choose retained files outside the agent state folders."}
	}
	return plan, nil
}

// ValidateShell checks one literal guest executable. It is never interpolated
// into a host shell, or interpreted as shell source.
func ValidateShell(name string) error {
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("sandbox.shell must name one executable. Try: use /bin/sh or another shell installed in the guest")
	}
	return nil
}

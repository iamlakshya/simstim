package host

import (
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/jail"
)

// converterSpec grants only actual final captured regular Files slots. Source
// directories stay in the guardian; no path bind, fd0 grant or stray integer
// becomes a capability in either converter operation.
func converterSpec(argv []string, files []*os.File) (jail.Spec, error) {
	if len(files) < 2 {
		return jail.Spec{}, fmt.Errorf("converter has no helper/output vector")
	}
	rules := make([]jail.FileRuleV1, 0, len(files))
	for index, file := range files {
		if file == nil {
			return jail.Spec{}, fmt.Errorf("converter has a missing captured descriptor")
		}
		info, err := file.Stat()
		if err != nil {
			return jail.Spec{}, err
		}
		if !info.Mode().IsRegular() {
			return jail.Spec{}, fmt.Errorf("converter inherited a non-regular descriptor")
		}
		rule := jail.FileRuleV1{FD: 3 + index, Role: jail.FileDataV1, Access: jail.Read, Mode: info.Mode().Perm()}
		switch index {
		case len(files) - 2:
			rule.Role = jail.FileProgramV1
			rule.Access = jail.Read | jail.Exec
		case len(files) - 1:
			rule.Access = jail.Read | jail.Write
		}
		rules = append(rules, rule)
	}
	return jail.Spec{Argv: argv, Files: files, FileRulesV1: rules, Net: jail.NetNone, Seccomp: jail.ProfileConverter}, nil
}

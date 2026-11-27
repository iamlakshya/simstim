//go:build linux

package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
)

type custodyAliasRetirementV1 struct {
	file      *os.File
	facts     supply.FileCloseFactsV1
	attempted bool
	logged    bool
}

// The concrete producer has joined every alias borrower before entering this
// method. Native retirement runs once on each original object; an unknown
// close keeps that object and is never retried through a reused FD number.

func (source *custodyKernelSourceV1) retireAliasFileV1(role string, file *os.File) error {
	if source.roleClosures == nil {
		source.roleClosures = make(map[string]*custodyAliasRetirementV1)
	}
	owned := source.roleClosures[role]
	if owned == nil {
		owned = &custodyAliasRetirementV1{file: file}
		source.roleClosures[role] = owned
	}
	if owned.file != file {
		return sessionLeaseError("the original custody descriptor was substituted during retirement")
	}
	if !owned.attempted {
		owned.attempted = true
		owned.facts = supply.CloseFileOnceV1(file)
	}
	if !owned.facts.Retired || owned.facts.Unknown != nil {
		return errors.Join(sessionLeaseError("the original custody descriptor has no confirmed native retirement"), owned.facts.Unknown)
	}
	if owned.facts.Diagnostic != nil && !owned.logged {
		warnSession(source.operation.Primary(), "original custody descriptor retired with a native diagnostic", "err", owned.facts.Diagnostic)
		owned.logged = true
	}
	return nil
}

func (source *custodyKernelSourceV1) closeProofAliasesV1() error {
	for role, file := range source.roles {
		if err := source.retireAliasFileV1(role, file); err != nil {
			return err
		}
		delete(source.roles, role)
	}
	return nil
}

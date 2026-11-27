//go:build !linux

package jail

import "github.com/iamlakshya/simstim/internal/publicationlease"

// RetainPublicationLeaseV1 admits no native helper lease off Linux.
func (*Cmd) RetainPublicationLeaseV1(*publicationlease.HolderV1) error { return errLinuxOnly }
func EnterPublicationConverterV1() error                               { return errLinuxOnly }

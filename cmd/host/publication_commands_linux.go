//go:build linux

package main

import (
	"os"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/jail"
)

func init() {
	commands["image-convert-publication-v1"] = func(args []string) error {
		if len(args) != 1 || (!closedImageCount(args[0]) && args[0] != host.SavedRootConverterMode) {
			return assetEntryError()
		}
		if err := jail.EnterPublicationConverterV1(); err != nil {
			return err
		}
		return host.RunImageConvert(args, os.Stdin, os.Stdout)
	}
	commands["agents-convert-publication-v1"] = func(args []string) error {
		if len(args) != 0 {
			return assetEntryError()
		}
		if err := jail.EnterPublicationConverterV1(); err != nil {
			return err
		}
		return host.RunAgentsConvert(args, os.Stdin, os.Stdout)
	}
}

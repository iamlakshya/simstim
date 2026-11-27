//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func init() {
	commands["image-guardian"] = runImageGuardianClosed
	commands["image-convert"] = runImageConvertClosed
	commands["agents-guardian"] = runAgentsGuardianClosed
	commands["agents-convert"] = runAgentsConvertClosed
}

func assetEntryError() error {
	return host.UserError("asset_entry", "the internal asset command has an invalid closed invocation", "Use the current helper's canonical image or installed-agents producer.")
}

func closedImageCount(value string) bool {
	count, err := strconv.ParseUint(value, 10, 32)
	return err == nil && count <= image.MaxLayers && strconv.FormatUint(count, 10) == value
}

func closedConverterUnit(value string) bool {
	token := strings.TrimSuffix(strings.TrimPrefix(value, brand.Name+"-image-"), ".scope")
	return value == brand.Name+"-image-"+token+".scope" && len(token) == 32 && supply.IsHex64(token+token)
}

func runImageGuardianClosed(args []string) error {
	if len(args) != 2 || (!closedImageCount(args[0]) && args[0] != host.SavedRootConverterMode) || !closedConverterUnit(args[1]) {
		return assetEntryError()
	}
	return host.RunImageGuardian(args)
}

func runImageConvertClosed(args []string) error {
	if len(args) != 1 || (!closedImageCount(args[0]) && args[0] != host.SavedRootConverterMode) {
		return assetEntryError()
	}
	return host.RunImageConvert(args, os.Stdin, os.Stdout)
}

func runAgentsGuardianClosed(args []string) error {
	if len(args) != 1 || !closedConverterUnit(args[0]) {
		return assetEntryError()
	}
	return host.RunAgentsGuardian(args)
}

func runAgentsConvertClosed(args []string) error {
	if len(args) != 0 {
		return assetEntryError()
	}
	return host.RunAgentsConvert(args, os.Stdin, os.Stdout)
}

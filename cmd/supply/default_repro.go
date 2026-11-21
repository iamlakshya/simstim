package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/iamlakshya/simstim/internal/image"
)

func defaultImageReproCmd(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("default-image repro", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	first := fs.String("first", "", "first actual prepared artifact directory")
	second := fs.String("second", "", "second actual prepared artifact directory")
	lockPath := fs.String("lock", "guest/supply.lock", "locked Debian build inputs")
	if err := fs.Parse(args); err != nil {
		return &failure{msg: err.Error(), try: "use guest/base-image/repro.sh <arch>"}
	}
	if *first == "" || *second == "" || *first == *second || fs.NArg() != 0 {
		return &failure{msg: "repro needs two distinct actual artifact directories", try: "use guest/base-image/repro.sh <arch> to build them independently"}
	}
	lock, err := loadLock(*lockPath)
	if err != nil {
		return err
	}
	result, err := image.CompareDefaultBuilds(context.Background(), *first, *second, lock)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

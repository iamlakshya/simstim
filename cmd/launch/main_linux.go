//go:build linux

package main

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

func main() {
	jail.Init()
	if err := secretmem.Protect(); err != nil {
		os.Exit(125)
	}
	slotlaunch.Init()
	os.Exit(slotlaunch.Serve(context.Background(), os.Stdin, os.Stdout))
}

//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "probe: the real guest fixture needs Linux. Try: cross-build test/sandboxprobe for the test host's architecture.")
	os.Exit(1)
}

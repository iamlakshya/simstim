//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "The slot launcher needs Linux.\nTry: prepare a Linux host for separate accounts.")
	os.Exit(1)
}

package client_test

import "github.com/iamlakshya/simstim/internal/sourcecompose"

// The SDK test binary uses the same actual application dependency graph on
// every platform. An external test package avoids client-to-host import cycles.
func init() {
	if err := sourcecompose.Install(); err != nil {
		panic(err)
	}
}

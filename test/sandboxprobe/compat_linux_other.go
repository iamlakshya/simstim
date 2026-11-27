//go:build linux && !amd64

package main

func compatUnshare() (int32, bool) { return -1, false }

//go:build linux && amd64

package main

func compatUnshareCall() int32

func compatUnshare() (int32, bool) { return compatUnshareCall(), true }

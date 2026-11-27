package main

import "github.com/iamlakshya/simstim/internal/guest"

func init() { commands[guest.SSHProxyHelperMode] = guest.RunSSHProxyHelper }

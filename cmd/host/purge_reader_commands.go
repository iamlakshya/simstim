package main

import "github.com/iamlakshya/simstim/internal/guest"

func init() {
	commands[guest.PurgeReaderGitHelperModeV1] = guest.RunPurgeReaderGitHelperV1
}

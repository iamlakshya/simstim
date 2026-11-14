//go:build linux

package cli

import "github.com/iamlakshya/simstim/internal/sourcecompose"

func init() { localGitSourceInit = sourcecompose.Install() }

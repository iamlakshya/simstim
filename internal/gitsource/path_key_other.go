//go:build !windows && !darwin

package gitsource

func sourcePathKey(name string) string { return name }

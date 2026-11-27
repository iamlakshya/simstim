//go:build !windows

package gitsource

func validateSourceSpelling(string) error { return nil }

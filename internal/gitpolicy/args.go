// Package gitpolicy defines the one non-executing Git policy shared by
// trusted host operations, guest bundles and laptop quarantine operations.
package gitpolicy

import "slices"

var hookEvents = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
	"prepare-commit-msg", "commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge",
	"pre-push", "pre-receive", "update", "proc-receive", "post-receive", "post-update",
	"reference-transaction", "push-to-checkout", "pre-auto-gc", "post-rewrite", "sendemail-validate",
	"fsmonitor-watchman", "p4-changelist", "p4-prepare-changelist", "p4-post-changelist",
	"p4-pre-submit", "post-index-change",
}

// HookEvents returns the hook events whose configuration must be disabled.
func HookEvents() []string { return slices.Clone(hookEvents) }

// Args returns a fresh copy of the canonical hook and fsmonitor arguments.
func Args() []string {
	args := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}
	for _, event := range hookEvents {
		args = append(args, "-c", "hook."+event+".enabled=false")
	}
	return args
}

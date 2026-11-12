package client

// Kind is the kind of an error. The exit code comes from it, never from the
// error's text (docs/contributing/cli-spec.md, section 5).
type Kind int

// The kinds of an error. The zero Kind is KindFailed.
const (
	// KindFailed: the command could not do its job.
	KindFailed Kind = iota
	// KindUsage: the command line or a config file is wrong.
	KindUsage
	// KindNotFound: a named thing does not exist.
	KindNotFound
	// KindNeedsAnswer: the command needs an answer, and input is off.
	KindNeedsAnswer
	// KindRefused: a policy or a tier refuses the action.
	KindRefused
	// KindCancelled: the user or a signal cancelled.
	KindCancelled
)

var kindNames = [...]string{"failed", "usage", "not_found", "needs_answer", "refused", "cancelled"}

// String is the kind's name, as machine output writes it: "failed",
// "usage", "not_found", "needs_answer", "refused" or "cancelled".
func (k Kind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return kindNames[KindFailed]
	}
	return kindNames[k]
}

// HostKind is the kind of an error code that a host sends. An unknown code
// is KindFailed.
func HostKind(code string) Kind {
	return hostKinds[code]
}

// UnknownNameError is the error of a name that does not exist, such as a
// host's. It carries the names that do exist, so that the client can
// suggest the nearest (docs/contributing/cli-spec.md 4.3).
type UnknownNameError struct {
	*UserError
	// Name is the name as the user typed it.
	Name string
	// Known holds the names that exist.
	Known []string
	// Flag and Arg say where the user typed Name: in the value of the flag
	// named Flag, or, when Flag is empty, in the argument of the command at
	// index Arg. The zero value is the first argument.
	Flag string
	Arg  int
	// Item is the index of Name in that word, when the word is a list
	// such as a,b (as split at its commas). Zero is its first item.
	Item int
}

func (e *UnknownNameError) Unwrap() error { return e.UserError }

// hostKinds gives each code that a host sends its kind. It holds every code
// of today's helper, and the codes of the isolation spec (cli-spec 5.3),
// which later helpers send. A code takes the kind that the table of
// cli-spec 5.1 gives its meaning, the same kind as the client's own check
// of the same mistake. Every other code is KindFailed (5.1, rule 6). A code
// that is not here is KindFailed, but a test fails on a code of today's
// helper that is missing.
var hostKinds = map[string]Kind{
	// Today's helper.
	"agent_missing":   KindFailed,
	"agent_running":   KindFailed,
	"alerts":          KindFailed,
	"attach_failed":   KindFailed,
	"bad_base":        KindFailed,
	"bad_folder":      KindFailed,
	"bad_name":        KindUsage,
	"bad_request":     KindFailed,
	"bad_secret":      KindUsage,
	"busy":            KindFailed,
	"debug_file":      KindFailed,
	"error":           KindFailed,
	"exited":          KindFailed,
	"exiting":         KindFailed,
	"forbidden":       KindRefused,
	"git_op":          KindFailed,
	"line_too_long":   KindFailed,
	"memory_max":      KindUsage,
	"no_folder":       KindNeedsAnswer,
	"no_git":          KindFailed,
	"no_recorder":     KindFailed,
	"not_a_repo":      KindFailed,
	"not_found":       KindNotFound,
	"profile":         KindFailed,
	"protocol":        KindFailed,
	"read":            KindFailed,
	"save":            KindFailed,
	"secrets_failed":  KindFailed,
	"secrets_missing": KindFailed,
	"session_gone":    KindFailed,
	"setup_trust":     KindNeedsAnswer,
	"start_failed":    KindFailed,
	"starting":        KindFailed,
	"too_big":         KindFailed,
	"unknown_agent":   KindNotFound,
	"unknown_op":      KindFailed,
	"upload":          KindFailed,
	"usage":           KindUsage,
	"worktree":        KindFailed,
	"write":           KindFailed,

	// The isolation spec (cli-spec 5.3).
	"wall_floor":            KindRefused,
	"wall_unavailable":      KindFailed,
	"wall_setup":            KindFailed,
	"wall_helper_old":       KindFailed,
	"policy_locked":         KindRefused,
	"layer_missing":         KindNeedsAnswer,
	"grant_wildcard":        KindRefused,
	"grant_idn":             KindRefused,
	"grant_public":          KindRefused,
	"secret_unbound":        KindRefused,
	"keep_refused":          KindRefused,
	"sandbox_stopped":       KindFailed,
	"sandbox_busy":          KindFailed,
	"unpulled":              KindFailed,
	"unsaved":               KindFailed,
	"pull_flagged":          KindRefused,
	"mount_microvm":         KindFailed,
	"digest_mismatch":       KindFailed,
	"judge_unavailable":     KindFailed,
	"ssh_key_kind":          KindUsage,
	"host_unknown":          KindUsage,
	"level_below":           KindFailed,
	"runtime_missing":       KindFailed,
	"no_room":               KindFailed,
	"draining":              KindRefused,
	"policy_unreached":      KindFailed,
	"secret_store":          KindFailed,
	"git_scope":             KindFailed,
	"git_hooks":             KindFailed,
	"git_timeout":           KindFailed,
	"git_version":           KindFailed,
	"lockdown_confirm":      KindNeedsAnswer,
	"setup_outside":         KindRefused,
	"setup_unreadable":      KindFailed,
	"image_bad_layer":       KindFailed,
	"image_root_mismatch":   KindFailed,
	"image_in_use":          KindFailed,
	"sandbox_boot":          KindFailed,
	"agents_disk":           KindFailed,
	"image_moved":           KindRefused,
	"image_signature":       KindRefused,
	"runtime_advisory":      KindRefused,
	"cp_too_big":            KindRefused,
	"cp_path":               KindRefused,
	"cp_git":                KindRefused,
	"cp_flagged":            KindRefused,
	"image_secret":          KindRefused,
	"image_flagged":         KindRefused,
	"image_elsewhere":       KindRefused,
	"cp_exists":             KindFailed,
	"rewind_off":            KindFailed,
	"image_exists":          KindFailed,
	"machine_token":         KindFailed,
	"machine_provider":      KindUsage,
	"rewind_unknown":        KindNotFound,
	"launcher_refused":      KindFailed,
	"image_wall":            KindFailed,
	"cp_unwalled":           KindFailed,
	"mount_repo_rw":         KindRefused,
	"image_too_large":       KindRefused,
	"release_age_invalid":   KindUsage,
	"package_level_invalid": KindUsage,
	"feed_unavailable":      KindFailed,
	"env_is_secret":         KindRefused,
	"copy_sensitive":        KindRefused,
	"sudo_wall":             KindRefused,
	"secret_not_here":       KindRefused,
	"no_host":               KindFailed,
	"invite_expired":        KindFailed,
	"invite_unknown":        KindFailed,
	"code_check":            KindUsage,
	"code_version":          KindFailed,
	"host_rule_layer":       KindUsage,
}

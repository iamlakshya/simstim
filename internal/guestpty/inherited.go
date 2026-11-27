package guestpty

import "github.com/iamlakshya/simstim/internal/brand"

// HolderFDEnv marks only the fixed inherited v2 data descriptor.
var HolderFDEnv = brand.Env("GUEST_PTY_V2_FD")

// HolderTokenEnv carries the opaque run binding, never a PID or credential.
var HolderTokenEnv = brand.Env("GUEST_PTY_V2_TOKEN")

// HookFDEnv marks init's already bound agent-owned hook listener.
var HookFDEnv = brand.Env("GUEST_HOOK_V2_FD")

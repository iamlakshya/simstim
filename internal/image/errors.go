// Package image pulls, flattens, converts and stores OCI session images and
// read-only agents disks for microvm sandboxes.
package image

import (
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Spec 20 error codes for image pulls, conversions, locking and agents disks.
const (
	CodeBadLayer     = "image_bad_layer"
	CodeTooLarge     = "image_too_large"
	CodeRootMismatch = "image_root_mismatch"
	CodeInUse        = "image_in_use"
	CodeMoved        = "image_moved"
	CodeDigest       = "digest_mismatch"
	CodeAgentsDisk   = "agents_disk"
	CodeNotFound     = "not_found"
)

// Error is a structured image error carrying a spec error code and a Try hint.
type Error struct {
	Code    string
	Message string
	Hint    string
	Err     error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

func badLayer(ref, detail string, err error) *Error {
	msg := fmt.Sprintf("a layer of %s is not valid: %s", refLabel(ref), detail)
	if detail == "" && err != nil {
		msg = fmt.Sprintf("a layer of %s is not valid: %v", refLabel(ref), err)
	}
	return &Error{
		Code:    CodeBadLayer,
		Message: msg,
		Hint:    "Rebuild the image, or use another one.",
		Err:     err,
	}
}

func tooLarge(ref, detail string) *Error {
	return &Error{
		Code:    CodeTooLarge,
		Message: fmt.Sprintf("%s exceeds the image limit: %s", refLabel(ref), detail),
		Hint:    "Use a smaller image.",
	}
}

func digestMismatch(ref, detail string, err error) *Error {
	return &Error{
		Code:    CodeDigest,
		Message: fmt.Sprintf("%s does not match its pinned digest: %s", refLabel(ref), detail),
		Hint:    "Run `" + brand.Name + " image pull` again.",
		Err:     err,
	}
}

func rootMismatch(ref, got, want string) *Error {
	return &Error{
		Code:    CodeRootMismatch,
		Message: fmt.Sprintf("%s gave root hash %s, expected %s; the host deleted its copy", refLabel(ref), shortHex(got), shortHex(want)),
		Hint:    "Run `" + brand.Name + " host doctor <host>`, and report both root hashes as a bug.",
	}
}

func imageInUse(digest, session string) *Error {
	return &Error{
		Code:    CodeInUse,
		Message: fmt.Sprintf("session %s still uses image %s", session, shortDigest(digest)),
		Hint:    "Remove that session with `" + brand.Name + " kill`, then remove the image.",
	}
}

func agentsDiskError(detail, hint string, err error) *Error {
	if hint == "" {
		hint = "Run `" + brand.Name + " host doctor <host>`."
	}
	return &Error{
		Code:    CodeAgentsDisk,
		Message: "could not build the agents disk: " + detail,
		Hint:    hint,
		Err:     err,
	}
}

// AsError extracts *Error from err when present.
func AsError(err error) (*Error, bool) {
	var ie *Error
	if errors.As(err, &ie) {
		return ie, true
	}
	return nil, false
}

func refLabel(ref string) string {
	if ref == "" {
		return "the image"
	}
	return ref
}

func shortHex(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}

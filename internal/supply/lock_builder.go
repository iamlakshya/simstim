package supply

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	builderImageForm = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*$`)
	builderSuiteForm = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	builderTimeForm  = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
)

func validateBuilder(b LockBuilder) error {
	ref, digest, ok := strings.Cut(b.Image, "@sha256:")
	if !ok || !builderImageForm.MatchString(ref) || !hex64.MatchString(digest) {
		return fmt.Errorf("guest/supply.lock: [builder] image %q must pin a container reference with @sha256:<64 hex>", b.Image)
	}
	if !builderTimeForm.MatchString(b.Snapshot) {
		return fmt.Errorf("guest/supply.lock: [builder] snapshot %q must be a UTC timestamp in YYYYMMDDTHHMMSSZ form", b.Snapshot)
	}
	if _, err := time.Parse("20060102T150405Z", b.Snapshot); err != nil {
		return fmt.Errorf("guest/supply.lock: [builder] snapshot %q is not a valid UTC date and time: %w", b.Snapshot, err)
	}
	if !builderSuiteForm.MatchString(b.Suite) {
		return fmt.Errorf("guest/supply.lock: [builder] suite %q must be a lower-case Debian suite name", b.Suite)
	}
	if !hex64.MatchString(b.InReleaseSHA256) {
		return fmt.Errorf("guest/supply.lock: [builder] inrelease_sha256 %q must be 64 lower-case hex digits", b.InReleaseSHA256)
	}
	if !hex64.MatchString(b.SecurityInReleaseSHA256) {
		return fmt.Errorf("guest/supply.lock: [builder] security_inrelease_sha256 %q must be 64 lower-case hex digits", b.SecurityInReleaseSHA256)
	}
	return nil
}

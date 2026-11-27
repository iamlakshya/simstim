package host

import (
	"fmt"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"os"
)

// quarantineFailure retains both paths when a safe no-clobber restore fails.
// It never grants authority to unlink the mismatched captured object.
type quarantineFailure struct {
	original, retained string
	cause, restore     error
	unmoved            bool
}

func (e *quarantineFailure) Error() string {
	if e.unmoved {
		return fmt.Sprintf("preserved %s and the unmatched existing capture %s: %v", e.original, e.retained, e.cause)
	}
	if e.restore != nil {
		return fmt.Sprintf("preserved %s in %s because restoring its original path would replace another object: %v", e.original, e.retained, e.cause)
	}
	return fmt.Sprintf("preserved %s after a changed ownership witness: %v", e.original, e.cause)
}
func (e *quarantineFailure) Unwrap() error { return e.cause }

var beforeQuarantineCapture = func() {}
var afterQuarantineCapture = func() {}

func removeCapturedFile(root *os.Root, leaf string, expected purgeRef, quarantine string, record func(string) error, check func(*os.File) error) error {
	mode := os.FileMode(expected.Mode)
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !expected.matches(before) {
		return placementError("the file changed before quarantine")
	}
	if check != nil {
		if err := check(file); err != nil {
			return err
		}
	}
	if err := record(quarantine); err != nil {
		return err
	}
	beforeQuarantineCapture()
	current, err := cachefs.ValidateFile(root, leaf, file, mode)
	if err != nil || !expected.matches(current) {
		return placementError("the file changed before its recorded move")
	}
	if err := cachefs.RenameNoReplace(root, leaf, quarantine); err != nil {
		return err
	}
	afterQuarantineCapture()
	captured, err := cachefs.OpenFile(root, quarantine, os.O_RDONLY, mode)
	if err == nil {
		defer captured.Close()
		info, statErr := captured.Stat()
		if statErr != nil || !expected.matches(info) {
			err = placementError("the captured file has another identity")
		}
		if err == nil && check != nil {
			err = check(captured)
		}
	}
	if err != nil {
		restoreErr := cachefs.RenameNoReplace(root, quarantine, leaf)
		return &quarantineFailure{original: leaf, retained: quarantine, cause: err, restore: restoreErr}
	}
	info, err := cachefs.ValidateFile(root, quarantine, captured, mode)
	if err != nil || !expected.matches(info) {
		cause := placementError("the quarantined file changed before unlink")
		return &quarantineFailure{original: leaf, retained: quarantine, cause: cause, restore: cachefs.RenameNoReplace(root, quarantine, leaf)}
	}
	if err := root.Remove(quarantine); err != nil {
		return err
	}
	return syncCapturedDirectory(root)
}

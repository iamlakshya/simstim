//go:build !linux

package host

import "context"

func activateManagerCustodyPreparationV1(context.Context, *SessionOperation, *Manifest) error {
	return resumePreparationErrorV1("native cold preparation is unavailable on this platform")
}

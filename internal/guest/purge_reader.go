package guest

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/proto"
)

const purgeReaderArgumentV1 = "--purge-reader-v1"
const PurgeReaderGitHelperModeV1 = "purge-reader-git-v1"

// Only the real Linux PID1 constructor installs this hook, after validating
// its tools root, different disk roles and original native host connection.
// Neither a request nor the ordinary VM prepare path can install it.
type purgeReaderHooksV1 interface {
	request(proto.Request, func(any, error))
	probe(context.Context, string, ...string) (string, int, error)
	wait(*execRun) (bool, error)
}

func purgeReaderRequestV1(operation string, raw json.RawMessage) error {
	if operation != proto.CtlPurgeWorkInspectV1 {
		return purgeWorkErrorV1("the fixed reader accepts only its protected work inspection")
	}
	_, err := decodePurgeWorkV1(raw)
	return err
}

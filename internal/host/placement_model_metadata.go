package host

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

type metadataWriteIntent struct {
	Before     *purgeRef `json:"before,omitempty"`
	After      purgeRef  `json:"after"`
	Temporary  string    `json:"temporary"`
	Quarantine string    `json:"quarantine,omitempty"`
	extra      map[string]json.RawMessage
}

func metadataWriteIntentKnown(intent metadataWriteIntent) bool {
	validRef := func(ref purgeRef) bool {
		return len(ref.extra) == 0 && ref.Inode != 0 && ref.UID == uint64(os.Getuid()) && ref.Mode == 0o600 && ref.Size >= 0 && ref.Size <= int64(proto.MaxFrame) && !ref.ModTime.IsZero()
	}
	return len(intent.extra) == 0 && validRef(intent.After) && (intent.Before == nil || validRef(*intent.Before)) && strings.HasPrefix(intent.Temporary, ".metadata-") && ValidHostID(strings.TrimPrefix(intent.Temporary, ".metadata-")) && (intent.Quarantine == "" || strings.HasPrefix(intent.Quarantine, ".metadata-cleanup-") && ValidHostID(strings.TrimPrefix(intent.Quarantine, ".metadata-cleanup-")))
}

func (intent *metadataWriteIntent) UnmarshalJSON(data []byte) error {
	type plain metadataWriteIntent
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"before", "after", "temporary", "quarantine"})
	*intent = metadataWriteIntent(value)
	intent.extra = extra
	return err
}

func (intent metadataWriteIntent) MarshalJSON() ([]byte, error) {
	type plain metadataWriteIntent
	return encodeFootprint(plain(intent), intent.extra)
}

// Package publicationlease preserves an original publication's native file
// lock through its trusted helpers. It grants no execution, ordinary file
// rule, storage adoption or operation-ledger authority.
package publicationlease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// GuardianFDV1 is the fixed private guardian handoff slot. A slot number or
// record cannot construct an original operation owner or grant execution.
const GuardianFDV1 = 200
const roleV1 = "image-publication-fence-v1"
const recordLimitV1 = 8192

// ErrBusy means an original inherited shared description still exists.
// Callers retry outside placement/publication/cache locks.
var ErrBusy = errors.New("the original publication still has native fence holders")

// RecordV1 is correlation data kept in the SAME canonical operation record.
// Only a checked native sidecar and a fresh exclusive lock can prove vacancy.
type RecordV1 struct {
	Version int               `json:"v"`
	Role    string            `json:"role"`
	Token   string            `json:"token"`
	Domain  cachefs.Domain    `json:"domain"`
	Root    cachefs.Reference `json:"root"`
	Self    cachefs.Reference `json:"self"`
}

func tokenV1(value string) bool {
	return len(value) == 32 && strings.Trim(value, "0123456789abcdef") == ""
}
func nameV1(token string) string { return "image-publication-" + token + ".lease" }
func failureV1(message string) error {
	return fmt.Errorf("%s\nTry: retain the original publication and inspect the host's native image cleanup.", message)
}
func (record RecordV1) checkV1() error {
	if record.Version != 1 || record.Role != roleV1 || !tokenV1(record.Token) || !record.Domain.Known() || !record.Root.Known() || !record.Root.Directory || record.Root.Mode != 0o700 || !record.Self.Known() || record.Self.Directory || record.Self.Mode != 0o600 {
		return failureV1("the publication fence has unknown origin or role data")
	}
	return nil
}

// DecodeRecordV1 parses only complete current canonical data. It issues no
// native lease, execution role or cleanup permission.
func DecodeRecordV1(value string) (RecordV1, error) {
	var record RecordV1
	if len(value) == 0 || len(value) > recordLimitV1 {
		return record, failureV1("the publication fence record exceeds its bound")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return record, failureV1("the publication fence record has trailing data")
	}
	if err := record.checkV1(); err != nil {
		return record, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return record, err
	}
	if string(encoded) != value {
		return record, failureV1("the publication fence record is duplicate or noncanonical")
	}
	return record, nil
}
func readRecordV1(file *os.File) (RecordV1, string, error) {
	var record RecordV1
	info, err := file.Stat()
	if err != nil {
		return record, "", err
	}
	if info.Size() <= 0 || info.Size() > recordLimitV1 {
		return record, "", failureV1("the native publication fence has no bounded record")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, info.Size()))
	if err != nil {
		return record, "", err
	}
	value := string(bytes.TrimSuffix(data, []byte{'\n'}))
	record, err = DecodeRecordV1(value)
	return record, value, err
}

// LeafV1 is the only token-derived sidecar basename for this current record.
// Callers still need native origin/reference checks and a fresh EX proof.
func (record RecordV1) LeafV1() (string, error) {
	if err := record.checkV1(); err != nil {
		return "", err
	}
	return nameV1(record.Token), nil
}

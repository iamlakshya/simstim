package proto

// CtlPurgeWorkInspectV1 selects one closed read of protected workspaces. It
// cannot start an agent, run caller-supplied argv, publish refs or record a pull.
const CtlPurgeWorkInspectV1 = "purge.work-inspect.v1"

// GuestPurgeReaderV1 identifies the fixed reader's different tools entry.
// It is a claim/compatibility marker, never a native boot or cleanup issuer.
const GuestPurgeReaderV1 = "purge-reader-v1"
const PurgeReaderResultLimitV1 = 48 << 10

// PurgeWorkRepositoryV1 contains only original host-protected repository and
// seed/pulled commit data. Names select /work/<name> inside the actual guest.
type PurgeWorkRepositoryV1 struct {
	Name    string   `json:"name"`
	Scratch bool     `json:"scratch"`
	Saved   []string `json:"saved"`
}

type PurgeWorkInspectArgsV1 struct {
	Version      int                     `json:"v"`
	Primary      string                  `json:"primary"`
	Repositories []PurgeWorkRepositoryV1 `json:"repositories"`
}

type PurgeWorkRepositoryResultV1 struct {
	Name     string `json:"name"`
	Dirty    bool   `json:"dirty"`
	Unpulled uint64 `json:"unpulled"`
}

// This is bounded work-loss DATA from the fixed guest reader. It is no native
// stop, cleanup, memory, execution or durable pull acknowledgement.
type PurgeWorkInspectResultV1 struct {
	Version             int                           `json:"v"`
	Primary             string                        `json:"primary"`
	OutsideRepositories bool                          `json:"outside_repositories"`
	Repositories        []PurgeWorkRepositoryResultV1 `json:"repositories"`
}

// PurgeReaderWorkResultV1 does not change ordinary guest inspection. The
// original native reader requires this closed ABI before checking Work against
// its SAME protected plan. Neither field grants stop, boot or disk authority.
type PurgeReaderWorkResultV1 struct {
	ABI  string                   `json:"abi"`
	Work PurgeWorkInspectResultV1 `json:"work"`
}

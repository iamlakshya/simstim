package proto

// FileTransferVersionV1 identifies the bounded file transfer wire contract.
const FileTransferVersionV1 = 1

// FeatureFileTransferV1 negotiates explicit upload destinations and get streams.
const FeatureFileTransferV1 = "file-transfer-v1"

// FileTransferChunkMaxV1 bounds a single copy read and its returned bytes.
const FileTransferChunkMaxV1 = 1 << 20

// FilePutHeaderV1 opens one put stream. An empty Path selects the paste folder.
type FilePutHeaderV1 struct {
	Version int    `json:"v"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path,omitempty"`
	Replace bool   `json:"replace,omitempty"`
}

// FilePutResultV1 confirms an exact, fully checked file, or gives its refusal.
type FilePutResultV1 struct {
	Version int    `json:"v"`
	Path    string `json:"path,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// FileGetHeaderV1 selects a bounded range of one rooted ordinary file.
type FileGetHeaderV1 struct {
	// List selects copy metadata; Offset and MaxBytes must both be zero.
	// The actual copy policy/schema bounds its result across stream frames.
	List bool `json:"list,omitempty"`
	Version  int    `json:"v"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"max_bytes"`
}

// CopyListArgsV1 selects a file or folder for a reviewed copy.
type CopyListArgsV1 struct {
	Version int    `json:"v"`
	ID      string `json:"id,omitempty"`
	Path    string `json:"path"`
}

// CopyListEntryV1 describes a relative ordinary file or folder beneath a root.
type CopyListEntryV1 struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	SHA256    string `json:"sha256,omitempty"`
}

// CopyListResultV1 describes the selected root, including an empty folder.
// A file root contains its basename; folder entries are relative beneath it.
type CopyListResultV1 struct {
	Version   int               `json:"v"`
	Name      string            `json:"name"`
	Directory bool              `json:"directory"`
	Entries   []CopyListEntryV1 `json:"entries"`
}

// CopyReadArgsV1 requests one bounded range through a dedicated get stream.
type CopyReadArgsV1 struct {
	Version  int    `json:"v"`
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"max_bytes"`
}

// FileGetResultV1 is the bounded first JSON line on a get stream. Size is the
// exact byte count that follows. A refusal has no following file bytes.
type FileGetResultV1 struct {
	Version int    `json:"v"`
	Size    int64  `json:"size"`
	Error   *Error `json:"error,omitempty"`
}

// CopyReadResultV1 returns one bounded range on the ordinary copy RPC.
type CopyReadResultV1 struct {
	Version int    `json:"v"`
	Data    []byte `json:"data"`
}

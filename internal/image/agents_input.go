package image

import (
	"io/fs"
	"os"
)

// AgentsInputV1 holds only actual already-open source capabilities selected
// from the server's canonical Agent table. It is never a wire DTO. Exactly one
// of Folder, File or LinkInfo is present; callers own and close their handles.
type AgentsInputV1 struct {
	HostPath string
	Folder   *os.Root
	File     *os.File
	// A top-level symlink remains metadata; its target is never traversed.
	LinkInfo   fs.FileInfo
	LinkTarget string
}

// AgentBinaryV1 names the disk's stable bin symlink to an explicitly resolved
// installed binary source. It grants no path traversal or execution authority.
type AgentBinaryV1 struct {
	Name         string
	ResolvedPath string
}

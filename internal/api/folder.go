package api

// CodeNoFolder is the error code of a new session whose folder does not exist
// on the host. The error's data is a FolderQuestion. The client asks the user,
// and starts again with NewArgs.MakeFolder if they say yes.
const CodeNoFolder = "no_folder"

// FolderQuestion asks the user whether a new session's folder may be made.
type FolderQuestion struct {
	// Folder is the missing folder on the host: a full path, with ~ expanded.
	Folder string `json:"folder"`
}

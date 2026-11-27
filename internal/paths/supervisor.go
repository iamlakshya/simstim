package paths

import "path/filepath"

// SupervisorLog is the log of a walled session's supervisor.
func SupervisorLog(id string) string { return filepath.Join(SessionDir(id), "supervisor.log") }

// GuestInitLog is the output of a walled session's guest init under the
// test driver. It is outside the sandbox's root, which each start makes
// anew, so it outlives a resume.
func GuestInitLog(id string) string { return filepath.Join(SessionDir(id), "guest-init.log") }

// GuestHolderLog keeps the log of a walled session's holder after its
// sandbox ends, under the test driver: the holder writes it in the
// sandbox, and the driver copies it here, so it outlives a resume.
func GuestHolderLog(id string) string { return filepath.Join(SessionDir(id), "guest-holder.log") }

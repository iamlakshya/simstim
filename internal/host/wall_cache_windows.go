package host

import "os"

// The host runtime is Unix only. A future Windows host must check the file's
// owner and ACL before accepting cached security capabilities.
func wallCacheOwned(os.FileInfo) bool { return false }

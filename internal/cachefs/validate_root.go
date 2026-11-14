package cachefs

import "os"

// ValidateRoot checks an already anchored directory with the canonical native
// owner, mode, ACL and link policy. It creates or changes nothing.
func ValidateRoot(root *os.Root) error { return validateRoot(root, nil) }

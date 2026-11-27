package host

import "errors"

// ErrManagerCustodyPassiveAbortV1 ends only the actual inherited passive
// bootstrap. It grants no Source, native closure, start or manager permission.
var ErrManagerCustodyPassiveAbortV1 = errors.New("the original passive custody bootstrap ended before factory release")

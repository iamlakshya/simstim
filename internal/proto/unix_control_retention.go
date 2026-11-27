package proto

import (
	"os"
	"sync"
)

// UnixControlRetentionV1 retains only actual failed native receive batches.
// It supplies resource custody, not descriptor roles, a Source or a close
// proof. Only the canonical receiver can populate it. The owning consumer
// must retire these exact objects through its original resource closer.
type UnixControlRetentionV1 struct {
	mu     sync.Mutex
	failed []*UnixControlV1
}

func (retention *UnixControlRetentionV1) retainFailedV1(control *UnixControlV1) {
	if len(control.Files) == 0 {
		return
	}
	retention.mu.Lock()
	defer retention.mu.Unlock()
	batch := *control
	batch.Files = append([]*os.File(nil), control.Files...)
	retention.failed = append(retention.failed, &batch)
}

// FailedFilesV1 returns the same materialised file objects, including every
// decoded prefix from a rejected receive. These are resource references only;
// a caller cannot populate this retention from file numbers or copied data.
func (retention *UnixControlRetentionV1) FailedFilesV1() []*os.File {
	if retention == nil {
		return nil
	}
	retention.mu.Lock()
	defer retention.mu.Unlock()
	var files []*os.File
	for _, control := range retention.failed {
		files = append(files, control.Files...)
	}
	return files
}

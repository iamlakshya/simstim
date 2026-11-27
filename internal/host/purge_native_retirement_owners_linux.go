//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"
)

func (owner *purgeNativeRetirementsV4) closeEndpointV4(endpoint *purgePrivateEndpointV2) error {
	if endpoint == nil {
		return nil
	}
	if owner != &endpoint.retirement {
		owner.mu.Lock()
		if owner.endpoints == nil {
			owner.endpoints = make(map[*purgePrivateEndpointV2]struct{})
		}
		owner.endpoints[endpoint] = struct{}{}
		owner.mu.Unlock()
	}
	return endpoint.Close()
}

func (owner *purgeNativeRetirementsV4) checkEndpointV4(endpoint *purgePrivateEndpointV2) error {
	if endpoint == nil {
		return nil
	}
	return endpoint.checkNativeRetiredV3(context.Background())
}

type purgeLineRetirementV4 struct {
	line       *StartupLineV1
	connection *net.UnixConn
	files      []*os.File
	failed     []*os.File
	pool       *nativeDescriptorRetirementsV1
	peer       *nativeStartupPeerV1
	candidate  *nativeStartupPeerV1
	unknown    error
}

// An active read/write gate is not a close attempt. The real owner interrupts
// its original I/O with deadlines and joins it before this first native close.
func (owner *purgeNativeRetirementsV4) closeStartupLineV4(line *StartupLineV1) error {
	if line == nil {
		return nil
	}
	owner.mu.Lock()
	record := owner.lines[line]
	owner.mu.Unlock()
	if record != nil {
		return errors.Join(owner.checkStartupLineV4(line), owner.diagnosticsV4())
	}
	if !line.readMu.TryLock() {
		return placementError("the actual original startup reader must join before its first native close")
	}
	defer line.readMu.Unlock()
	if !line.writeMu.TryLock() {
		return placementError("the actual original startup writer must join before its first native close")
	}
	defer line.writeMu.Unlock()
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.reading {
		return placementError("the actual startup read still owns native resources")
	}
	if line.nativeRetirement != owner.descriptorOwnerV4() || line.receiveRetention == nil {
		return placementError("the original startup line lost its immutable first-read retirement owner")
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return placementError("the original purge line has no native Unix resource owner")
	}
	record = &purgeLineRetirementV4{line: line, connection: connection, files: append([]*os.File(nil), line.files...), failed: line.receiveRetention.FailedFilesV1(), pool: line.nativeRetirement, peer: line.nativePeer}
	if line.candidate != nil {
		record.candidate = line.candidate.peer
	}
	if line.closed {
		record.unknown = placementError("the original startup line was closed without retained native first-close facts")
	}
	owner.mu.Lock()
	if owner.lines == nil {
		owner.lines = make(map[*StartupLineV1]*purgeLineRetirementV4)
	}
	owner.lines[line] = record
	owner.mu.Unlock()
	if record.unknown != nil {
		return record.unknown
	}
	line.closed = true
	line.files, line.cred = nil, nil
	line.closeErr = errors.Join(owner.closeConnectionV4(connection), owner.closeFilesV4(record.files), owner.closeFilesV4(record.failed), owner.closePeerV4(record.peer), owner.closePeerV4(record.candidate))
	return line.closeErr
}

func (owner *purgeNativeRetirementsV4) checkStartupLineV4(line *StartupLineV1) error {
	if line == nil {
		return nil
	}
	owner.mu.Lock()
	record := owner.lines[line]
	owner.mu.Unlock()
	if record == nil || record.line != line || record.unknown != nil || line.conn != record.connection || line.nativePeer != record.peer || record.pool != owner.descriptorOwnerV4() || line.nativeRetirement != record.pool || line.receiveRetention == nil {
		return placementError("the original startup line has no retained same-object native retirement")
	}
	failed := line.receiveRetention.FailedFilesV1()
	if len(failed) != len(record.failed) {
		return placementError("the actual native failed-batch set changed after its physical reader joined")
	}
	for index, file := range failed {
		if file != record.failed[index] {
			return placementError("the original failed native materialisation object changed")
		}
	}
	return errors.Join(owner.checkConnectionV4(record.connection), owner.checkFilesV4(record.files), owner.checkFilesV4(record.failed), record.pool.checkAllRetiredV4(), owner.checkPeerV4(record.peer), owner.checkPeerV4(record.candidate))
}

package supply

import (
	"encoding/binary"
	"fmt"
	"io"
)

const ExecModeRecordBytesV1 = 112
const ExecModeProjectionV1 = 1
const ExecModeArmedV1 = 2

// ExecModeRecordV1 is bounded correlation DATA from the ONE original native
// sentinel. It grants nothing without the actual Cmd/source, creation/code/
// ancestry/adoption proof and original reader. PID is correlation only.
type ExecModeRecordV1 struct {
	Phase                                      byte
	ProgramDev, ProgramIno, LeaseDev, LeaseIno uint64
	Nonce                                      [16]byte
	FSVerity                                   [32]byte
	PID                                        uint64
}

func ReadExecModeRecordV1(reader io.Reader) (ExecModeRecordV1, error) {
	var record ExecModeRecordV1
	var raw [ExecModeRecordBytesV1]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return record, err
	}
	if string(raw[:8]) != "VMMMODE1" || raw[8] != 1 || (raw[9] != ExecModeProjectionV1 && raw[9] != ExecModeArmedV1) {
		return record, fmt.Errorf("unknown closed VMM mode record")
	}
	for _, b := range append(append([]byte(nil), raw[10:16]...), raw[104:112]...) {
		if b != 0 {
			return record, fmt.Errorf("VMM mode record has unknown reserved fields")
		}
	}
	record.Phase = raw[9]
	record.ProgramDev = binary.LittleEndian.Uint64(raw[16:24])
	record.ProgramIno = binary.LittleEndian.Uint64(raw[24:32])
	record.LeaseDev = binary.LittleEndian.Uint64(raw[32:40])
	record.LeaseIno = binary.LittleEndian.Uint64(raw[40:48])
	copy(record.Nonce[:], raw[48:64])
	copy(record.FSVerity[:], raw[64:96])
	record.PID = binary.LittleEndian.Uint64(raw[96:104])
	if record.PID == 0 || record.PID > 1<<31-1 {
		return record, fmt.Errorf("VMM mode record has no bounded PID correlation")
	}
	return record, nil
}

func writeExecModeRecordV1(writer io.Writer, record ExecModeRecordV1) error {
	var raw [ExecModeRecordBytesV1]byte
	copy(raw[:8], "VMMMODE1")
	raw[8], raw[9] = 1, record.Phase
	binary.LittleEndian.PutUint64(raw[16:24], record.ProgramDev)
	binary.LittleEndian.PutUint64(raw[24:32], record.ProgramIno)
	binary.LittleEndian.PutUint64(raw[32:40], record.LeaseDev)
	binary.LittleEndian.PutUint64(raw[40:48], record.LeaseIno)
	copy(raw[48:64], record.Nonce[:])
	copy(raw[64:96], record.FSVerity[:])
	binary.LittleEndian.PutUint64(raw[96:104], record.PID)
	n, err := writer.Write(raw[:])
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}

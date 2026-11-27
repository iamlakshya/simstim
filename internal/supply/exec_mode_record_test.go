package supply

import (
	"bytes"
	"context"
	"testing"
)

func TestExecModeRecordIsClosedBoundedDataAndZeroLeaseRefuses(t *testing.T) {
	record := ExecModeRecordV1{Phase: ExecModeProjectionV1, ProgramDev: 1, ProgramIno: 2, LeaseDev: 3, LeaseIno: 4, PID: 12}
	var wire bytes.Buffer
	if err := writeExecModeRecordV1(&wire, record); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != ExecModeRecordBytesV1 {
		t.Fatal("mode record changed its closed size")
	}
	decoded, err := ReadExecModeRecordV1(bytes.NewReader(wire.Bytes()))
	if err != nil || decoded != record {
		t.Fatal(decoded, err)
	}
	for _, kind := range []string{"version", "phase", "reserved", "short"} {
		data := append([]byte(nil), wire.Bytes()...)
		switch kind {
		case "version":
			data[8] = 2
		case "phase":
			data[9] = 0
		case "reserved":
			data[110] = 1
		case "short":
			data = data[:len(data)-1]
		}
		if _, err := ReadExecModeRecordV1(bytes.NewReader(data)); err == nil {
			t.Fatal("unknown mode data was accepted", kind)
		}
	}
	zero := new(ExecModeLeaseV1)
	if zero.CheckProgramV1(context.Background(), nil) == nil || zero.ArmV1(context.Background()) == nil || zero.RestoreFinalV1(context.Background()) == nil {
		t.Fatal("zero data minted a native mode phase")
	}
	if file, err := zero.RetainForJailV1(context.Background()); err == nil || file != nil {
		t.Fatal("zero source minted a private OFD")
	}
}

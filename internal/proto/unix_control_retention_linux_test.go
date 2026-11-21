//go:build linux

package proto

import (
	"os"
	"testing"
)

func TestOwnedNativeReceiveRetainsRejectedMaterialisedPrefix(t *testing.T) {
	receiver, sender := witnessPairV1(t)
	if err := EnableUnixCredentialsV1(receiver); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "retained-right-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SendUnixControlV1(sender, []byte{'x'}, []*os.File{file, file, file}); err != nil {
		t.Fatal(err)
	}
	retention := &UnixControlRetentionV1{}
	var one [1]byte
	_, control, err := ReceiveUnixControlRetainedV1(receiver, one[:], 1, retention)
	if err == nil || len(control.Files) == 0 {
		t.Fatal("bounded rejection did not preserve its actual materialised prefix", err)
	}
	files := retention.FailedFilesV1()
	if len(files) != len(control.Files) {
		t.Fatal("the original failed batch lost materialised objects")
	}
	for i, received := range files {
		defer received.Close()
		if received != control.Files[i] {
			t.Fatal("retention replaced an actual received file object")
		}
		if _, err := received.Stat(); err != nil {
			t.Fatal("lower rejection closed a file before its original retirement owner", err)
		}
	}
	control.Files[0] = file
	if retention.FailedFilesV1()[0] != files[0] {
		t.Fatal("a returned slice changed the retained original batch")
	}
}

func TestOwnedNativeReceiveSuccessKeepsActualWitnessAndTransfers(t *testing.T) {
	receiver, sender := witnessPairV1(t)
	if err := EnableUnixCredentialsV1(receiver); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "transferred-right-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SendUnixControlV1(sender, []byte{'x'}, []*os.File{file}); err != nil {
		t.Fatal(err)
	}
	retention := &UnixControlRetentionV1{}
	var one [1]byte
	_, control, err := ReceiveUnixControlRetainedV1(receiver, one[:], 1, retention)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.CheckReceivedV1(receiver); err != nil {
		t.Fatal("owned receive lost its actual native witness", err)
	}
	files, err := control.DetachFilesV1()
	if err != nil || len(files) != 1 {
		t.Fatal("owned receive changed its original transfer", err)
	}
	defer files[0].Close()
	if len(retention.FailedFilesV1()) != 0 {
		t.Fatal("successful claimed resources remained in the failed-batch owner")
	}
	if _, err := files[0].Stat(); err != nil {
		t.Fatal("transfer prematurely closed the real file", err)
	}
}

func TestLegacyNativeReceiveStillClosesRejectedBatch(t *testing.T) {
	receiver, sender := witnessPairV1(t)
	if err := EnableUnixCredentialsV1(receiver); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "legacy-right-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SendUnixControlV1(sender, []byte{'x'}, []*os.File{file, file, file}); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	_, control, err := ReceiveUnixControlV1(receiver, one[:], 1)
	if err == nil || len(control.Files) != 0 {
		t.Fatal("nil-owner legacy cleanup or exact bound changed", err)
	}
}

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/fsbirth"
)

func noneDirectoryDataFixtureV1(t *testing.T) Manifest {
	t.Helper()
	m := noneCleanFixtureV1()
	data := noneDirectoryCreationV1{Version: 1, Primary: m.ID, Boot: m.Run.BootID, Generation: m.Run.Generation, Parent: m.NoneCleanup.Directory, Directory: m.NoneCleanup.Directory, ParentBirth: &fsbirth.DataV2{Version: 2, Filesystem: 1, FSID: [2]int32{1, 2}, Type: 1, Handle: []byte{1}}, DirectoryBirth: &fsbirth.DataV2{Version: 2, Filesystem: 1, FSID: [2]int32{1, 2}, Type: 1, Handle: []byte{2}}}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	m.NoneDirectoryCreation = raw
	return m
}

func TestNoneDirectoryCreationDataIsFiniteAndDoesNotIssueSubjects(t *testing.T) {
	m := noneDirectoryDataFixtureV1(t)
	if _, err := decodeNoneDirectoryCreationV1(&m); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Manifest){
		"unknown": func(m *Manifest) {
			m.NoneDirectoryCreation = bytes.Replace(m.NoneDirectoryCreation, []byte(`"v":1`), []byte(`"v":1,"unknown":true`), 1)
		},
		"duplicate": func(m *Manifest) {
			m.NoneDirectoryCreation = bytes.Replace(m.NoneDirectoryCreation, []byte(`"v":1`), []byte(`"v":1,"v":1`), 1)
		},
		"alias": func(m *Manifest) {
			m.NoneDirectoryCreation = bytes.Replace(m.NoneDirectoryCreation, []byte(`"primary":`), []byte(`"Primary":`), 1)
		},
		"future": func(m *Manifest) {
			m.NoneDirectoryCreation = bytes.Replace(m.NoneDirectoryCreation, []byte(`"v":1`), []byte(`"v":2`), 1)
		},
		"wrong-run":  func(m *Manifest) { m.Run.Generation = "new-run" },
		"wrong-role": func(m *Manifest) { m.Wall.Level = "process" },
		"legacy":     func(m *Manifest) { m.NoneDirectoryCreation = nil },
	} {
		t.Run(name, func(t *testing.T) {
			copy := CloneManifest(m)
			edit(&copy)
			if _, err := decodeNoneDirectoryCreationV1(&copy); err == nil {
				t.Fatal("unknown directory DATA was accepted")
			}
		})
	}
	copy := CloneManifest(m)
	copy.NoneDirectoryCreation[0] = '['
	if m.NoneDirectoryCreation[0] != '{' {
		t.Fatal("the clone aliases original directory DATA")
	}
	if _, err := beginNoneStoredDeleteV1(context.Background(), nil, nil); err == nil {
		t.Fatal("directory DATA without an actual Delete issued a storage subject")
	}
}

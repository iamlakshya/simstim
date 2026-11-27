package filecopy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func copyReadJSONV1(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func copyListJSONV1(t *testing.T, size int64) []byte {
	return copyReadJSONV1(t, proto.CopyListResultV1{Version: 1, Name: "notes", Entries: []proto.CopyListEntryV1{{Path: "notes", Mode: "file", Size: size}}})
}
func requireCopyCodeV1(t *testing.T, err error, code string) {
	t.Helper()
	var wire *proto.Error
	if !errors.As(ReadErrorV1(err), &wire) || wire.Code != code || wire.Hint == "" {
		t.Fatalf("want %s with hint, got %v", code, err)
	}
}

func TestReadSessionUsesOriginalListingOffsetsAndExplicitEOF(t *testing.T) {
	calls, completed := 0, 0
	reader, err := NewReadSessionV1("primary2", 4, func(_ context.Context, header proto.FileGetHeaderV1) ([]byte, error) {
		calls++
		if header.Path != "/home/agent/notes" {
			t.Fatal("expanded source changed", header)
		}
		if header.List {
			return copyListJSONV1(t, 4), nil
		}
		switch header.Offset {
		case 0:
			return []byte("ab"), nil
		case 2:
			return []byte("cd"), nil
		case 4:
			return nil, nil
		}
		t.Fatal("unexpected source offset", header)
		return nil, nil
	}, func(name string, size int64) {
		completed++
		if name != "/home/agent/notes" || size != 4 {
			t.Fatal(name, size)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	list := proto.CopyListArgsV1{Version: 1, ID: "primary2", Path: "~/notes"}
	if _, err := reader.List(t.Context(), copyReadJSONV1(t, list)); err != nil {
		t.Fatal(err)
	}
	args := proto.CopyReadArgsV1{Version: 1, ID: "primary2", Path: "~/notes", MaxBytes: 2}
	for _, offset := range []int64{0, 2, 4} {
		args.Offset = offset
		result, err := reader.Read(t.Context(), copyReadJSONV1(t, args))
		if err != nil {
			t.Fatal(err)
		}
		if offset < 4 && len(result.Data) != 2 {
			t.Fatal("short source range")
		}
		if offset == 4 && (len(result.Data) != 0 || completed != 1) {
			t.Fatal("missing exact terminal EOF")
		}
	}
	before := calls
	if _, err := reader.Read(t.Context(), copyReadJSONV1(t, args)); err == nil {
		t.Fatal("terminal range replayed")
	}
	if calls != before || completed != 1 {
		t.Fatal("replay entered source or repeated completion")
	}
}

func TestReadSessionRejectsDifferentIdentityOrRangeBeforeSource(t *testing.T) {
	calls := 0
	reader, err := NewReadSessionV1("primary2", 4, func(_ context.Context, header proto.FileGetHeaderV1) ([]byte, error) {
		calls++
		return copyListJSONV1(t, 4), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.List(t.Context(), copyReadJSONV1(t, proto.CopyListArgsV1{Version: 1, ID: "primary2", Path: "/work/notes"})); err != nil {
		t.Fatal(err)
	}
	for _, args := range []proto.CopyReadArgsV1{{Version: 1, ID: "changed2", Path: "/work/notes", MaxBytes: 1}, {Version: 1, ID: "primary2", Path: "/work/elsewhere", MaxBytes: 1}, {Version: 1, ID: "primary2", Path: "/work/notes", Offset: 1, MaxBytes: 1}, {Version: 1, ID: "primary2", Path: "/work/notes", MaxBytes: 5}, {Version: 1, ID: "primary2", Path: "/work/notes", MaxBytes: proto.FileTransferChunkMaxV1 + 1}} {
		before := calls
		_, err := reader.Read(t.Context(), copyReadJSONV1(t, args))
		requireCopyCodeV1(t, err, "cp_path")
		if calls != before {
			t.Fatal("changed selection entered source")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	before := calls
	if _, err := reader.Read(canceled, copyReadJSONV1(t, proto.CopyReadArgsV1{Version: 1, ID: "primary2", Path: "/work/notes", MaxBytes: 1})); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatal("canceled read entered source")
	}
}

func TestReadSessionKeepsWholeCopyPolicyBound(t *testing.T) {
	for _, size := range []int64{5, 1 << 40} {
		reader, err := NewReadSessionV1("primary2", 4, func(context.Context, proto.FileGetHeaderV1) ([]byte, error) { return copyListJSONV1(t, size), nil }, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.List(t.Context(), copyReadJSONV1(t, proto.CopyListArgsV1{Version: 1, ID: "primary2", Path: "/work/notes"}))
		requireCopyCodeV1(t, err, "cp_too_big")
		if reader.files != nil {
			t.Fatal("oversized list published a partial read plan")
		}
	}
}

func TestReadSessionRejectsChangedSizeAndEarlyEOFWithoutProgress(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("overflow")} {
		reader, err := NewReadSessionV1("primary2", 4, func(_ context.Context, header proto.FileGetHeaderV1) ([]byte, error) {
			if header.List {
				return copyListJSONV1(t, 4), nil
			}
			return data, nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.List(t.Context(), copyReadJSONV1(t, proto.CopyListArgsV1{Version: 1, ID: "primary2", Path: "/work/notes"})); err != nil {
			t.Fatal(err)
		}
		_, err = reader.Read(t.Context(), copyReadJSONV1(t, proto.CopyReadArgsV1{Version: 1, ID: "primary2", Path: "/work/notes", MaxBytes: 4}))
		requireCopyCodeV1(t, err, "cp_too_big")
		if file := reader.files["/work/notes"]; file.offset != 0 || file.done {
			t.Fatal("invalid source bytes advanced original read plan")
		}
	}
}

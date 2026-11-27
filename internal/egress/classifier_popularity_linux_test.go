//go:build linux

package egress

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func preparedPopularityFixtureV1(t *testing.T) (*PopularityPreparedV1, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rank.csv")
	if err := os.WriteFile(path, []byte("rank,domain\n12,example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := CapturePopularityV1(t.Context(), original, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	if _, err := original.Stat(); err == nil {
		t.Fatal("capture did not consume its original descriptor")
	}
	return prepared, path
}

func adoptPreparedPopularityFixtureV1(t *testing.T, prepared *PopularityPreparedV1, id string, mutate func(*popularityBootstrapV1)) *PopularitySourceV1 {
	t.Helper()
	fd, err := unix.FcntlInt(prepared.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta popularityBootstrapV1
	if err := json.Unmarshal(prepared.header, &meta); err != nil {
		t.Fatal(err)
	}
	meta.SessionID = id
	if mutate != nil {
		mutate(&meta)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := adoptPopularityFilesV1(t.Context(), id, os.NewFile(uintptr(fd), "popularity-fixture"), bytes.NewReader(raw))
	if err != nil {
		if mutate != nil {
			return nil
		}
		t.Fatal(err)
	}
	if reader != nil {
		t.Cleanup(func() { _ = reader.Close() })
	}
	return reader
}

func TestPopularityOriginalFactoryPinsSnapshotAndNeverReadsReplacementPath(t *testing.T) {
	prepared, path := preparedPopularityFixtureV1(t)
	server, _ := judgeCapabilityFixture(t)
	reader := adoptPreparedPopularityFixtureV1(t, prepared, server.currentMembers().metadata.SessionID, nil)
	if err := server.bindPopularitySourceV1(reader); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.file.WriteAt([]byte("9"), 0); err == nil {
		t.Fatal("sealed popularity snapshot permitted mutation")
	}
	if err := reader.file.Truncate(0); err == nil {
		t.Fatal("sealed popularity snapshot permitted truncation")
	}
	flags, err := unix.FcntlInt(reader.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("fact descriptor could reach a later configured judge command")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("999,example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := judgeSourceContext(t, server)
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{Popularity: &path}}}
	actual, _ := server.collectClassifierFacts(ctx, Request{Host: "docs.example.com", Port: 443}, policy)
	if actual.Popularity.Status != classify.FactKnown || actual.Popularity.Rank != 12 {
		t.Fatal("lookup reopened a replacement instead of its original snapshot")
	}
	changed := filepath.Join(t.TempDir(), "new.csv")
	policy.Classifier.Popularity = &changed
	actual, _ = server.collectClassifierFacts(ctx, Request{Host: "docs.example.com", Port: 443}, policy)
	if actual.Popularity.Status != classify.FactUnknown || actual.Popularity.Rank != 0 {
		t.Fatal("hot policy metadata selected an unissued file source")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if actual := reader.rank(ctx, server, path, "example.com"); actual.Status != classify.FactUnknown {
		t.Fatal("closed source reopened a path")
	}
}

func TestPopularityActualSealedFDAndExactBootstrapAreBothRequired(t *testing.T) {
	prepared, path := preparedPopularityFixtureV1(t)
	if reader := adoptPreparedPopularityFixtureV1(t, prepared, "bound234", func(meta *popularityBootstrapV1) {
		meta.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	}); reader != nil {
		t.Fatal("changed private bootstrap accepted a different snapshot")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta popularityBootstrapV1
	if err := json.Unmarshal(prepared.header, &meta); err != nil {
		t.Fatal(err)
	}
	meta.SessionID = "bound234"
	meta.Snapshot, err = popularityFileVersionV1Of(file)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if reader, err := adoptPopularityFilesV1(t.Context(), "bound234", file, bytes.NewReader(raw)); err == nil || reader != nil {
		t.Fatal("an unsealed file plus matching-looking DTO issued a reader")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("rejected descriptor was retained")
	}
	reader := adoptPreparedPopularityFixtureV1(t, prepared, "bound234", nil)
	server, _ := judgeCapabilityFixture(t)
	if err := server.bindPopularitySourceV1(reader); err != nil {
		t.Fatal(err)
	}
	other, _ := judgeCapabilityFixture(t)
	if err := other.bindPopularitySourceV1(reader); err == nil {
		t.Fatal("another actor borrowed the same descriptor source")
	}
	if actual := reader.rank(t.Context(), server, path, "example.com"); actual.Status != classify.FactUnknown {
		t.Fatal("a context without the actual source issuer read facts")
	}
}

func TestPopularityPreparedHandoffClosesBeforeAnyChild(t *testing.T) {
	prepared, _ := preparedPopularityFixtureV1(t)
	link, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	cmd := exec.Command("/unused/helper", "egress", "bound234")
	cmd.ExtraFiles = []*os.File{link}
	if err := prepared.AppendEgressCommandV1(t.Context(), cmd, "bound234"); err != nil {
		t.Fatal(err)
	}
	if len(cmd.ExtraFiles) != 3 || cmd.Args[len(cmd.Args)-1] != "--popularity-fds-v1=4" {
		t.Fatal("actual inherited handoff layout changed")
	}
	if err := prepared.AppendEgressCommandV1(t.Context(), cmd, "bound234"); err == nil {
		t.Fatal("one original source produced another handoff")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.file.Stat(); err == nil {
		t.Fatal("parent snapshot survived its closed handoff")
	}
}

func TestPopularityActualPipeHandoffIsBoundBeforeInit(t *testing.T) {
	prepared, _ := preparedPopularityFixtureV1(t)
	link, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	cmd := exec.Command("/unused/helper", "egress", "bound234")
	cmd.ExtraFiles = []*os.File{link}
	if err := prepared.AppendEgressCommandV1(t.Context(), cmd, "bound234"); err != nil {
		t.Fatal(err)
	}
	fileFD, err := unix.FcntlInt(cmd.ExtraFiles[1].Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	headerFD, err := unix.FcntlInt(cmd.ExtraFiles[2].Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		unix.Close(fileFD)
		t.Fatal(err)
	}
	header := os.NewFile(uintptr(headerFD), "inherited-bootstrap-fixture")
	defer header.Close()
	source, err := adoptPopularityFilesV1(t.Context(), "bound234", os.NewFile(uintptr(fileFD), "inherited-snapshot-fixture"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.sessionID != "bound234" || source.selected == "" {
		t.Fatal("actual inherited bootstrap lost original binding")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.file.Stat(); err != nil {
		t.Fatal("parent-copy close invalidated the actual inherited source", err)
	}
}

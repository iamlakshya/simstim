package host

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestImageSaveSourceRequiresOriginalNativeOperationBeforeAnyProducer(t *testing.T) {
	if source, err := CaptureImageSaveSource(t.Context(), context.Background(), nil, nil, nil, nil); err == nil || source != nil {
		t.Fatal("public context created a native image source")
	}
	var source ImageSaveSource
	writes := 0
	if pending, err := source.Prepare(t.Context(), image.SaveRequest{Ref: "tools"}, nil, func(context.Context, io.Writer) error { writes++; return nil }); err == nil || pending != nil || writes != 0 {
		t.Fatal("an unissued source started a root producer")
	}
	if source.Check(t.Context()) == nil || source.Close() == nil {
		t.Fatal("an empty source gained a native lifetime")
	}
}

func TestImageSaveScanUnionIncludesProtectedLiveHistoricalAndDecoyValues(t *testing.T) {
	root := compactPublicationFixture()
	live := api.CloneSessionSecretState(root.SecretState)
	public := live.Primary["KEY"]
	public.Sentinel = "LIVE0001"
	live.Primary["KEY"] = public
	live.Retired = map[string][]api.RetiredPrimarySecretState{"KEY": {{Sentinel: "HIST0002", Shape: public.Shape, Identity: strings.Repeat("d", 32), Binding: live.Intent.Binds["KEY"]}}}
	child := &Manifest{ID: "def23456", SandboxOf: root.ID, Secrets: []string{"CHILD"}}
	definitions, err := proto.DecoyDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	var decoys []api.DecoyTripwire
	for _, definition := range definitions {
		tripwire := api.DecoyTripwire{Name: definition.Name}
		for _, field := range definition.Values {
			tripwire.Values = append(tripwire.Values, api.Secret(field.Prefix+strings.Repeat(field.Alphabet[:1], field.Length-len(field.Prefix))))
		}
		decoys = append(decoys, tripwire)
	}
	values, names, err := imageSaveScanUnion(root, []*Manifest{child}, live, decoys)
	if err != nil || !names["KEY"] || !names["CHILD"] {
		t.Fatal("the original source lost its declared name domain", err)
	}
	var found []api.Secret
	for _, value := range values {
		found = append(found, value)
	}
	for _, wanted := range []api.Secret{"STND0000", "LIVE0001", "HIST0002"} {
		if !slices.Contains(found, wanted) {
			t.Fatal("an original current or historical sentinel was omitted")
		}
	}
	for _, tripwire := range decoys {
		for _, wanted := range tripwire.Values {
			if !slices.Contains(found, wanted) {
				t.Fatal("an original decoy was omitted")
			}
		}
	}
	public.Sentinel = "EDIT0009"
	live.Primary["KEY"] = public
	if slices.Contains(found, "EDIT0009") {
		t.Fatal("the scanner union borrowed mutable public metadata")
	}
	live.Version++
	if _, _, err := imageSaveScanUnion(root, []*Manifest{child}, live, decoys); err == nil {
		t.Fatal("unknown public state silently disabled the scan")
	}
}

package supervisor

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/iamlakshya/simstim/internal/guest"
)

type decoyEntropyProbe struct {
	reads int
	err   error
}

func (r *decoyEntropyProbe) Read(data []byte) (int, error) {
	r.reads++
	if r.err != nil {
		return 0, r.err
	}
	for index := range data {
		data[index] = 255
	}
	return len(data), nil
}

func TestDecoysUseFreshValuesAndTheCanonicalFormatsAtEachStart(t *testing.T) {
	firstFiles, first, err := newDecoys(t.Context(), true)
	if err != nil || guest.ValidateDecoyFilesV1(firstFiles) != nil || len(first) != 2 {
		t.Fatal("first decoy generation is incomplete", err)
	}
	secondFiles, second, err := newDecoys(t.Context(), true)
	if err != nil || guest.ValidateDecoyFilesV1(secondFiles) != nil || len(second) != len(first) {
		t.Fatal("second decoy generation is incomplete", err)
	}
	for index := range first {
		if len(first[index].Values) == 0 || len(first[index].Hosts) == 0 || first[index].Name != second[index].Name {
			t.Fatal("decoy tripwire lost canonical names, hosts or values")
		}
		for value := range first[index].Values {
			if first[index].Values[value] == second[index].Values[value] {
				t.Fatal("a cold start reused a prior decoy value")
			}
		}
		if firstFiles.Files[index].Content == secondFiles.Files[index].Content {
			t.Fatal("a cold start reused a decoy file")
		}
	}
}

func TestDisabledDecoysReadNoEntropyAndRegisterNoFilesOrValues(t *testing.T) {
	entropy := &decoyEntropyProbe{err: errors.New("must not read")}
	files, tripwires, err := makeDecoys(t.Context(), false, entropy)
	if err != nil || files != nil || tripwires != nil || entropy.reads != 0 {
		t.Fatalf("disabled decoys generated material: files=%t tripwires=%d reads=%d err=%v", files != nil, len(tripwires), entropy.reads, err)
	}
}

func TestDecoyGenerationFailureReturnsNoPartialFilesOrTripwires(t *testing.T) {
	for _, mode := range []string{"entropy", "rejection", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entropy := &decoyEntropyProbe{}
			if mode == "entropy" {
				entropy.err = io.ErrUnexpectedEOF
			}
			if mode == "cancelled" {
				cancel()
			}
			files, tripwires, err := makeDecoys(ctx, true, entropy)
			if err == nil || files != nil || tripwires != nil {
				t.Fatal("an incomplete decoy generation escaped")
			}
			if mode == "cancelled" && entropy.reads != 0 || entropy.reads > 5000 {
				t.Fatalf("decoy entropy reads escaped cancellation or the fixed budget: %d", entropy.reads)
			}
		})
	}
}

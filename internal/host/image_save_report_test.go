package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func denseSavedChanges() []string {
	changes := make([]string, proto.ImageChangesMax)
	for index := range changes {
		changes[index] = fmt.Sprintf("/usr/local/bin/tool-%06d-%s", index, strings.Repeat("x", 32))
	}
	return changes
}

func TestSavedChangesPreludePreservesFullVectorOutsideTheJSONPlan(t *testing.T) {
	changes := denseSavedChanges()
	plan, err := image.NewSavedRootPlan(files.StatePlan{}, files.StatePlan{}, "/bin", nil, changes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > 1024 || bytes.Contains(encoded, []byte("tool-000000")) {
		t.Fatal("advisory paths still occupy the fixed JSON plan", err)
	}
	var input bytes.Buffer
	if err := writeSavedChanges(&input, changes); err != nil {
		t.Fatal(err)
	}
	if input.Len() <= proto.MaxFrame {
		t.Fatal("the prelude fixture does not cross the original plan boundary")
	}
	input.WriteByte(99) // The first raw-tar byte belongs to the next reader.
	actual, err := readSavedChanges(&input)
	if err != nil || !reflect.DeepEqual(actual, changes) || input.Len() != 1 {
		t.Fatal("the bounded prelude changed paths, order or its own extent", err)
	}
	var invalid bytes.Buffer
	_ = binary.Write(&invalid, binary.LittleEndian, uint32(proto.ImageChangesMax+1))
	if _, err := readSavedChanges(&invalid); err == nil {
		t.Fatal("an oversized declared vector entered the worker")
	}
}

func TestSavedWorkerAndGuardianPagesPreserveReviewBeyondTheOldWholeResultCap(t *testing.T) {
	review := image.SaveReview{ID: strings.Repeat("a", 32), Ref: "tools", Size: 4096, Excluded: []image.SaveFinding{{Path: "/work", Reason: "runtime filesystem"}}}
	for _, name := range denseSavedChanges() {
		review.Flagged = append(review.Flagged, image.SaveFinding{Path: name, Reason: "file in the original image PATH"})
	}
	var worker bytes.Buffer
	for offset := 0; ; {
		page, err := review.PageV1(offset)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(&worker).Encode(savedRootWorkerFrame{Version: 1, Page: &page, DroppedDevices: 1}); err != nil {
			t.Fatal(err)
		}
		if page.Done {
			break
		}
		offset = page.Next
	}
	if worker.Len() <= int(image.MaxManifest) {
		t.Fatal("the worker fixture does not cross its former whole result cap")
	}
	var guardian bytes.Buffer
	final, err := readSavedRootWorker(&worker, json.NewEncoder(&guardian))
	if err != nil {
		t.Fatal(err)
	}
	if len(final) > 1024 {
		t.Fatal("the final owned outcome still holds the whole review")
	}
	if err := json.NewEncoder(&guardian).Encode(imageGuardianFrame{Phase: "done", Output: final}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames, joined := imageSaveGuardianFrames(ctx, &guardian)
	var collector image.SaveReviewCollectorV1
	finished := false
	for result := range frames {
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.frame.Phase {
		case "review":
			var page image.SaveReviewPageV1
			if err := json.Unmarshal(result.frame.Output, &page); err != nil {
				t.Fatal(err)
			}
			if err := collector.Append(page, "tools"); err != nil {
				t.Fatal(err)
			}
		case "done":
			finished = true
		default:
			t.Fatal("an unrelated frame entered the saved review")
		}
	}
	<-joined
	actual, err := collector.Review()
	if err != nil || !finished || !reflect.DeepEqual(actual, review) {
		t.Fatal("a bounded worker/guardian carrier lost review data", err)
	}
}

func TestSavedGuardianPageReaderJoinsAfterBackpressureCancellation(t *testing.T) {
	var input bytes.Buffer
	for range 8 {
		_ = json.NewEncoder(&input).Encode(imageGuardianFrame{Phase: "review", Output: []byte(`{}`)})
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, joined := imageSaveGuardianFrames(ctx, &input)
	cancel()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("the original reader stayed blocked on an abandoned frame queue")
	}
	// An overlong unterminated frame is refused with bounded storage.
	frames, done := imageSaveGuardianFrames(t.Context(), io.LimitReader(strings.NewReader(strings.Repeat("x", 70<<10)), 70<<10))
	result := <-frames
	if result.err == nil {
		t.Fatal("an overlong frame entered the report carrier")
	}
	<-done
}

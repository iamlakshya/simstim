package egress

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/files"
)

// TestJudgeEvaluation is deliberately opt-in. Its reviewed corpus stays in
// private storage: observed destinations must never become public fixtures.
// This harness reports measurements; it sets no model quality threshold.
func TestJudgeEvaluation(t *testing.T) {
	if os.Getenv("JUDGE_EVAL") != "1" {
		t.Skip("set JUDGE_EVAL=1 with a reviewed corpus and configured judge program")
	}
	corpus, program, model := os.Getenv("JUDGE_EVAL_CORPUS"), os.Getenv("JUDGE_EVAL_COMMAND"), os.Getenv("JUDGE_EVAL_MODEL")
	if corpus == "" || program == "" || model == "" {
		t.Fatal("JUDGE_EVAL needs CORPUS, COMMAND and MODEL. Try: name a reviewed private corpus, host executable and actual model version")
	}
	file := openPrivateEvaluationSource(t, corpus)
	defer file.Close()
	cases, digest, err := classify.ReadEvaluationCorpus(file)
	if err != nil {
		t.Fatal(err)
	}
	judge, err := classify.NewCommand(program, "session")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify})
	if err != nil {
		t.Fatal(err)
	}
	report, err := classify.RunEvaluation(t.Context(), cases, digest, model, judge, func(input classify.Input) (classify.Result, error) {
		request := Request{Host: input.Host, Port: input.Port, Method: input.Method, Path: input.Path}
		decision := engine.Decide(request)
		if decision.Rule == "classify:unavailable" {
			return classify.Result{}, errors.New("fixed classifier unavailable")
		}
		action := "unsure"
		if !decision.Allow && strings.HasPrefix(decision.Rule, "classify:") && decision.Rule != "classify:unsure" && decision.Rule != "classify:unavailable" {
			action = "deny"
		}
		barriers, err := serviceBarriersFor(request)
		if err != nil {
			return classify.Result{}, err
		}
		for _, barrier := range barriers {
			if barrier.classifier {
				action = "deny"
			}
		}
		return classify.Result{Action: action, Scope: "session"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if destination := os.Getenv("JUDGE_EVAL_REPORT"); destination != "" {
		writePrivateEvaluationJSON(t, destination, report)
	}
	t.Logf("cases=%d independently-reviewed-benign=%d incident-derived=%d incidents=%d", report.Cases, report.Observed, report.Hostile, report.Incidents)
	t.Logf("fixed-only wrong-allow=%d/%d wrong-deny=%d/%d unsure=%d", report.Fixed.WrongAllow, report.Hostile, report.Fixed.WrongDeny, report.Observed, report.Fixed.Unsure)
	t.Logf("fixed-and-judge wrong-allow=%d/%d wrong-deny=%d/%d unsure=%d unavailable=%d", report.Combined.WrongAllow, report.Hostile, report.Combined.WrongDeny, report.Observed, report.Combined.Unsure, report.Combined.Unavailable)
}

// TestJudgeObservationImport is a separate, network-free opt-in intake step.
// It emits unlabelled candidates for independent review, never an eval corpus.
func TestJudgeObservationImport(t *testing.T) {
	if os.Getenv("JUDGE_EVAL_INGEST") != "1" {
		t.Skip("set JUDGE_EVAL_INGEST=1 for private unlabelled audit intake")
	}
	source, destination := os.Getenv("JUDGE_EVAL_AUDIT"), os.Getenv("JUDGE_EVAL_CANDIDATES")
	if source == "" || destination == "" {
		t.Fatal("audit intake needs AUDIT and CANDIDATES. Try: select the private source log and a new private output path")
	}
	file := openPrivateEvaluationSource(t, source)
	defer file.Close()
	observations, digest, err := classify.ReadObservedTraffic(file, classify.Metadata{}, "reviewed-private-audit")
	if err != nil {
		t.Fatal(err)
	}
	writePrivateEvaluationJSON(t, destination, struct {
		Version      int                              `json:"version"`
		SourceSHA256 string                           `json:"source_sha256"`
		Observations []classify.EvaluationObservation `json:"observations"`
	}{1, digest, observations})
	t.Logf("imported %d unlabelled candidate observations; independent benign/incident labels and a basis are still required", len(observations))
}

func openPrivateEvaluationSource(t *testing.T, source string) *os.File {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		t.Fatal("cannot open private evaluation source directory. Try: select an existing private source")
	}
	defer root.Close()
	file, err := files.OpenRegular(root, filepath.Base(source))
	if err != nil {
		t.Fatal("cannot open regular private evaluation source. Try: select a regular file with no symlink components")
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm()&0077 != 0 || info.Size() > classify.MaxCorpusBytes {
		_ = file.Close()
		t.Fatal("evaluation source must be private and at most 16 MiB. Try: review a bounded source with mode 0600")
	}
	return file
}

func writePrivateEvaluationJSON(t *testing.T, destination string, value any) {
	t.Helper()
	if !filepath.IsAbs(destination) {
		t.Fatal("evaluation output needs an absolute private path. Try: select a new file inside private/")
	}
	parts := strings.Split(filepath.Clean(destination), string(filepath.Separator))
	privateIndex := -1
	for index, part := range parts {
		if index > 1 && index < len(parts)-1 && part == "private" {
			privateIndex = index
		}
	}
	if privateIndex < 0 {
		t.Fatal("evaluation output needs a private directory. Try: select a new file inside private/")
	}
	privateRoot := string(filepath.Separator) + filepath.Join(parts[1:privateIndex+1]...)
	root, err := os.OpenRoot(privateRoot)
	if err != nil {
		t.Fatal("cannot open private evaluation output directory. Try: create the intended private directory first")
	}
	defer root.Close()
	name := filepath.Join(parts[privateIndex+1:]...)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("cannot exclusively create private evaluation output. Try: choose a new private file name")
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = root.Remove(name)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if encoder.Encode(value) != nil || file.Sync() != nil || file.Close() != nil {
		t.Fatal("cannot finish private evaluation output. Try: check free space and retry with a new private file name")
	}
	complete = true
}

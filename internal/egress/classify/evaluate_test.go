package classify

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type evaluationJudge func(context.Context, Input) (Result, error)

func (judge evaluationJudge) Decide(ctx context.Context, input Input) (Result, error) {
	return judge(ctx, input)
}

func unitEvaluationCase(index int) EvaluationCase {
	input := testInput()
	input.Host = "unit-" + strconv.Itoa(index) + ".invalid"
	input.RegistrableDomain = ""
	sample := EvaluationCase{ID: "unit-" + strconv.Itoa(index), Input: input, Want: "allow", Label: "benign", Origin: "observed-traffic", Source: "synthetic unit fixture", Basis: "controlled unit expectation; not an observed session"}
	if index >= 200 {
		sample.Want, sample.Label, sample.Origin, sample.Incident = "deny", "incident", "lookalike", "unit-incident"
	}
	return sample
}

func TestObservedAuditImportMakesUnlabelledCandidatesAndPreservesProvenance(t *testing.T) {
	raw := `{"v":1,"kind":"net","host":"docs.example.com","port":443,"mode":"intercept","method":"GET","path":"/docs","decision":"allow"}` + "\n" +
		`{"v":1,"kind":"net","host":"docs.example.com","port":443,"mode":"intercept","method":"POST","path":"/other","decision":"allow"}` + "\n" +
		`{"v":1,"kind":"net","host":"refused.example.com","port":443,"decision":"deny"}` + "\n"
	observed, digest, err := ReadObservedTraffic(strings.NewReader(raw), Metadata{}, "private audit source")
	if err != nil || len(observed) != 1 || len(digest) != 64 || observed[0].Source != "private audit source#line=1" {
		t.Fatal("observation provenance or normalised destination dedup failed", err)
	}
	encoded, _ := json.Marshal(observed)
	if strings.Contains(string(encoded), `"want"`) || strings.Contains(string(encoded), `"label"`) || strings.Contains(string(encoded), `"basis"`) {
		t.Fatal("allowed traffic was automatically labelled benign")
	}
}

func TestEvaluationCorpusRequiresIndependentLabelsBasisAndCanonicalCaseIDs(t *testing.T) {
	valid := unitEvaluationCase(0)
	for _, mutate := range []func(*EvaluationCase){
		func(sample *EvaluationCase) { sample.Label = "" },
		func(sample *EvaluationCase) { sample.Basis = "" },
		func(sample *EvaluationCase) { sample.ID = "" },
		func(sample *EvaluationCase) { sample.Want = "unsure" },
		func(sample *EvaluationCase) { sample.Label = "incident" },
	} {
		sample := valid
		mutate(&sample)
		raw, _ := json.Marshal(sample)
		if _, _, err := ReadEvaluationCorpus(strings.NewReader(string(raw) + "\n")); err == nil {
			t.Fatal("unlabelled or ambiguous observation counted as ground truth")
		}
	}
	first, second := unitEvaluationCase(0), unitEvaluationCase(1)
	first.ID, second.ID = "Case_ONE", "case--one"
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if _, _, err := ReadEvaluationCorpus(strings.NewReader(string(a) + "\n" + string(b) + "\n")); err == nil {
		t.Fatal("case identifier aliases inflated corpus size")
	}
	second.ID, second.Input.Host = "different-id", strings.ToUpper(first.Input.Host)+"."
	a, _ = json.Marshal(first)
	b, _ = json.Marshal(second)
	if _, _, err := ReadEvaluationCorpus(strings.NewReader(string(a) + "\n" + string(b) + "\n")); err == nil {
		t.Fatal("relabelled or changed-path destination inflated corpus size")
	}
}

func TestEvaluationRefusesIncompleteCorpusBeforeAnyJudgeCall(t *testing.T) {
	judge := evaluationJudge(func(context.Context, Input) (Result, error) {
		t.Fatal("incomplete corpus made a judge call")
		return Result{}, nil
	})
	if _, err := RunEvaluation(t.Context(), []EvaluationCase{unitEvaluationCase(0)}, strings.Repeat("a", 64), "unit-fake", judge, func(Input) (Result, error) { return Result{Action: "unsure"}, nil }); err == nil {
		t.Fatal("incomplete corpus generated an accuracy report")
	}
}

func TestEvaluationRunnerSyntheticUnitCasesAreNotModelMeasurements(t *testing.T) {
	// These .invalid fixtures exercise counters only. They are never saved
	// as an observed corpus and make no program, API or model request.
	cases := make([]EvaluationCase, 400)
	for index := range cases {
		cases[index] = unitEvaluationCase(index)
	}
	judge := evaluationJudge(func(_ context.Context, input Input) (Result, error) {
		index, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(input.Host, "unit-"), ".invalid"))
		if index == 1 {
			return Result{}, errors.New("controlled unit failure")
		}
		action := "allow"
		if index >= 200 {
			action = "deny"
		}
		if index == 0 {
			action = "deny"
		}
		if index == 200 {
			action = "allow"
		}
		return Result{Action: action, Scope: "session", Reason: "unit reason omitted from reports"}, nil
	})
	report, err := RunEvaluation(t.Context(), cases, strings.Repeat("a", 64), "unit-fake", judge, func(Input) (Result, error) { return Result{Action: "unsure"}, nil })
	if err != nil || report.Cases != 400 || report.Observed != 200 || report.Hostile != 200 || report.Incidents != 1 || report.Combined.WrongAllow != 1 || report.Combined.WrongDeny != 1 || report.Combined.Unavailable != 1 || report.Combined.Unsure != 1 || report.Fixed.Unsure != 400 {
		t.Fatal("unit counters changed", err)
	}
	raw, _ := json.Marshal(report)
	for _, forbidden := range []string{".invalid", "synthetic unit fixture", "unit reason", `"input"`, `"source"`, `"basis"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("metrics report contained individual case data")
		}
	}
}

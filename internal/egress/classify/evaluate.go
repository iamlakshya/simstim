package classify

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

const (
	MaxCorpusBytes = 16 << 20
	MaxCorpusCases = 10000
)

var errEvaluation = errors.New("judge evaluation input is incomplete, ambiguous or unbounded. Try: provide reviewed private JSON lines with unique destinations and source records")

// EvaluationCase keeps provenance outside the model's structured input.
// Source names the reviewed observation or incident, never a model message.
type EvaluationCase struct {
	ID       string `json:"id"`
	Input    Input  `json:"input"`
	Want     string `json:"want"`
	Label    string `json:"label"`
	Origin   string `json:"origin"`
	Source   string `json:"source"`
	Basis    string `json:"basis"`
	Incident string `json:"incident,omitempty"`
}

// EvaluationObservation is an unlabelled candidate. An allowed audit event
// establishes an observation, not benign ground truth or an expected action.
type EvaluationObservation struct {
	Input            Input  `json:"input"`
	Source           string `json:"source"`
	ObservedDecision string `json:"observed_decision"`
}

// EvaluationCounts keeps unsure and unavailable results visible rather than
// counting them as a successful deny. Wrong-allow/deny denominators are the
// report's Hostile and Observed counts respectively.
type EvaluationCounts struct {
	Allowed     int `json:"allowed"`
	Denied      int `json:"denied"`
	Unsure      int `json:"unsure"`
	Unavailable int `json:"unavailable"`
	WrongAllow  int `json:"wrong_allow"`
	WrongDeny   int `json:"wrong_deny"`
}

// EvaluationReport contains metrics and corpus identity, never destinations,
// task lines, source paths, individual reasons or private error output.
type EvaluationReport struct {
	Version      int              `json:"version"`
	Model        string           `json:"model"`
	CorpusSHA256 string           `json:"corpus_sha256"`
	Cases        int              `json:"cases"`
	Observed     int              `json:"observed"`
	Hostile      int              `json:"hostile"`
	Incidents    int              `json:"incidents"`
	Fixed        EvaluationCounts `json:"fixed"`
	Combined     EvaluationCounts `json:"fixed_and_judge"`
	DurationMS   int64            `json:"duration_ms"`
}

// ReadEvaluationCorpus ingests an existing reviewed private source. It does
// not invent, relabel or repeat destinations to meet an evaluation minimum.
func ReadEvaluationCorpus(reader io.Reader) ([]EvaluationCase, string, error) {
	var cases []EvaluationCase
	seen := make(map[string]bool)
	ids := make(map[string]bool)
	digest, err := evaluationLines(reader, func(raw []byte, _ int) error {
		var sample EvaluationCase
		if !api.ExactJSONFields(raw, sample) || json.Unmarshal(raw, &sample) != nil {
			return errEvaluation
		}
		var err error
		sample.Input.Host, err = nettarget.NormaliseHost(sample.Input.Host)
		if err != nil || !validEvaluationCase(sample) {
			return errEvaluation
		}
		key, id := evaluationDestination(sample.Input), canonicalEvidenceID(sample.ID)
		if seen[key] || ids[id] || len(cases) >= MaxCorpusCases {
			return errEvaluation
		}
		seen[key] = true
		ids[id] = true
		cases = append(cases, sample)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return cases, digest, nil
}

// ReadObservedTraffic converts actual allowed net audit records into private
// UNLABELLED candidates. Refused flows are not observations of allowed traffic.
// Metadata is supplied from that source's protected start; absent facts stay
// unknown or disabled. No fact provider is reimplemented here.
func ReadObservedTraffic(reader io.Reader, metadata Metadata, source string) ([]EvaluationObservation, string, error) {
	if source == "" || !textData(source, 2048) {
		return nil, "", errEvaluation
	}
	var cases []EvaluationObservation
	seen := make(map[string]bool)
	digest, err := evaluationLines(reader, func(raw []byte, line int) error {
		var record api.NetAuditEntry
		if !api.ExactJSONFields(raw, record) || json.Unmarshal(raw, &record) != nil {
			return errEvaluation
		}
		if record.Kind != "net" || record.Decision != "allow" && record.Decision != "allow-unknown" {
			return nil
		}
		if record.V != 1 {
			return errEvaluation
		}
		input := Input{Version: JudgeVersion, Host: record.Host, Port: record.Port,
			Facts:            Facts{DomainAge: AgeFact{Status: FactDisabled}, Popularity: RankFact{Status: FactDisabled}},
			RepositoryRemote: metadata.RepositoryRemote, Ecosystems: append([]string(nil), metadata.Ecosystems...), Agent: metadata.Agent, Profile: metadata.Profile, Task: metadata.Task}
		if record.Mode == "intercept" && record.Path != "" && !strings.ContainsAny(record.Path, "[]?#") {
			input.Method, input.Path = record.Method, record.Path
		}
		var err error
		input.Host, err = nettarget.NormaliseHost(input.Host)
		if err != nil {
			return nil
		}
		if _, err := EncodeInput(input); err != nil {
			return nil
		} // Omitted or scrubbed destinations supply no case.
		key := evaluationDestination(input)
		if seen[key] {
			return nil
		}
		if len(cases) >= MaxCorpusCases {
			return errEvaluation
		}
		seen[key] = true
		cases = append(cases, EvaluationObservation{Input: input, Source: source + "#line=" + strconv.Itoa(line), ObservedDecision: record.Decision})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return cases, digest, nil
}

func evaluationLines(reader io.Reader, visit func([]byte, int) error) (string, error) {
	if reader == nil {
		return "", errEvaluation
	}
	limited := &io.LimitedReader{R: reader, N: MaxCorpusBytes + 1}
	digest := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(limited, digest))
	scanner.Buffer(make([]byte, 4096), 2*MaxInputBytes)
	line := 0
	for scanner.Scan() {
		line++
		if len(scanner.Bytes()) == 0 || visit(scanner.Bytes(), line) != nil {
			return "", errEvaluation
		}
	}
	if scanner.Err() != nil || limited.N == 0 {
		return "", errEvaluation
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func evaluationDestination(input Input) string {
	// A repeated host with a changed task, path, method or case identifier
	// cannot inflate the required destination count.
	return input.Host + "\x00" + strconv.Itoa(input.Port)
}

func validEvaluationCase(sample EvaluationCase) bool {
	if canonicalEvidenceID(sample.ID) == "" || sample.Source == "" || !textData(sample.Source, 2048) || sample.Basis == "" || !textData(sample.Basis, 4096) {
		return false
	}
	if _, err := EncodeInput(sample.Input); err != nil {
		return false
	}
	return sample.Want == "allow" && sample.Label == "benign" && sample.Origin == "observed-traffic" && sample.Incident == "" ||
		sample.Want == "deny" && sample.Label == "incident" && canonicalEvidenceID(sample.Incident) != "" && (sample.Origin == "incident" || sample.Origin == "lookalike")
}

func canonicalEvidenceID(value string) string {
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), "-")
	value = strings.ReplaceAll(value, "_", "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	for _, c := range []byte(value) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == ':') {
			return ""
		}
	}
	return value
}

// RunEvaluation validates the complete corpus before making any judge call.
// baseline is the actual fixed classifier owner, not a duplicate classifier.
// This function supplies no session policy or permission to publish a rule.
func RunEvaluation(ctx context.Context, cases []EvaluationCase, digest, model string, judge Judge, baseline func(Input) (Result, error)) (EvaluationReport, error) {
	report := EvaluationReport{Version: 1, Model: model, CorpusSHA256: digest, Cases: len(cases)}
	if ctx == nil || judge == nil || baseline == nil || model == "" || !textData(model, 256) || len(cases) > MaxCorpusCases || len(digest) != 64 {
		return EvaluationReport{}, errEvaluation
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return EvaluationReport{}, errEvaluation
	}
	seen := make(map[string]bool)
	ids, incidents := make(map[string]bool), make(map[string]bool)
	cases = slices.Clone(cases)
	for index := range cases {
		sample := &cases[index]
		var err error
		sample.Input.Host, err = nettarget.NormaliseHost(sample.Input.Host)
		if err != nil {
			return EvaluationReport{}, errEvaluation
		}
		sample.Input.Ecosystems = slices.Clone(sample.Input.Ecosystems)
		key, id := evaluationDestination(sample.Input), canonicalEvidenceID(sample.ID)
		if !validEvaluationCase(*sample) || seen[key] || ids[id] {
			return EvaluationReport{}, errEvaluation
		}
		seen[key] = true
		ids[id] = true
		if sample.Want == "allow" {
			report.Observed++
		} else {
			report.Hostile++
		}
		if sample.Incident != "" {
			incidents[canonicalEvidenceID(sample.Incident)] = true
		}
	}
	report.Incidents = len(incidents)
	if report.Observed < 200 || report.Hostile < 200 {
		return EvaluationReport{}, errEvaluation
	}
	started := time.Now()
	for _, sample := range cases {
		if ctx.Err() != nil {
			return EvaluationReport{}, errEvaluation
		}
		fixed, err := baseline(sample.Input)
		if err != nil {
			report.Fixed.Unavailable++
			report.Combined.Unavailable++
			countEvaluation(&report.Fixed, "unsure", sample.Want)
			countEvaluation(&report.Combined, "unsure", sample.Want)
			continue
		}
		if fixed.Action != "allow" && fixed.Action != "deny" && fixed.Action != "unsure" {
			return EvaluationReport{}, errEvaluation
		}
		countEvaluation(&report.Fixed, fixed.Action, sample.Want)
		action := fixed.Action
		if action == "unsure" {
			call, cancel := context.WithTimeout(ctx, 5*time.Second)
			result, err := judge.Decide(call, sample.Input)
			if err == nil {
				result, err = BoundResult(result, "session")
			}
			if err != nil || call.Err() != nil {
				report.Combined.Unavailable++
			} else {
				action = result.Action
			}
			cancel()
		}
		countEvaluation(&report.Combined, action, sample.Want)
	}
	if ctx.Err() != nil {
		return EvaluationReport{}, errEvaluation
	}
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

func countEvaluation(counts *EvaluationCounts, action, want string) {
	switch action {
	case "allow":
		counts.Allowed++
		if want == "deny" {
			counts.WrongAllow++
		}
	case "deny":
		counts.Denied++
		if want == "allow" {
			counts.WrongDeny++
		}
	default:
		counts.Unsure++
	}
}

package egress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp/syntax"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type serviceMintFixture struct{ id, value string }

func serviceMintFixtures() []serviceMintFixture {
	return []serviceMintFixture{
		{"github-app-token", "ghu_" + strings.Repeat("A", 36)},
		{"github-app-token", "ghs_" + strings.Repeat("B", 36)},
		{"github-fine-grained-pat", "github_pat_" + strings.Repeat("A", 82)},
		{"github-oauth", "gho_" + strings.Repeat("A", 36)},
		{"github-pat", "ghp_" + strings.Repeat("A", 36)},
		{"github-refresh-token", "ghr_" + strings.Repeat("A", 36)},
		{"gitlab-cicd-job-token", "glcbt-ABCDE_" + strings.Repeat("A", 20)},
		{"gitlab-deploy-token", "gldt-" + strings.Repeat("A", 20)},
		{"gitlab-feature-flag-client-token", "glffct-" + strings.Repeat("A", 20)},
		{"gitlab-feed-token", "glft-" + strings.Repeat("A", 20)},
		{"gitlab-incoming-mail-token", "glimt-" + strings.Repeat("A", 25)},
		{"gitlab-kubernetes-agent-token", "glagent-" + strings.Repeat("A", 50)},
		{"gitlab-oauth-app-secret", "gloas-" + strings.Repeat("A", 64)},
		{"gitlab-pat", "glpat-" + strings.Repeat("A", 20)},
		{"gitlab-pat-routable", "glpat-" + strings.Repeat("A", 300) + ".ab1234567"},
		{"gitlab-ptt", "glptt-" + strings.Repeat("a", 40)},
		{"gitlab-rrt", "GR1348941" + strings.Repeat("A", 20)},
		{"gitlab-runner-authentication-token", "glrt-" + strings.Repeat("A", 20)},
		{"gitlab-runner-authentication-token-routable", "glrt-t7_" + strings.Repeat("A", 300) + ".ab1234567"},
		{"gitlab-scim-token", "glsoat-" + strings.Repeat("A", 20)},
		{"npm-access-token", "nPm_" + strings.Repeat("A", 36)},
		{"pypi-upload-token", "pypi-AgEIcHlwaS5vcmc" + strings.Repeat("A", 1000)},
	}
}

func TestServiceMintSourceBoundsAndOriginalModelContractRemainExact(t *testing.T) {
	model, err := parsePinnedResponseRules(responseMintedData)
	if err != nil || len(model.rules) != 3 || model.window != 170 || responsePatternWindow != 170 {
		t.Fatal("the original model three-rule/170-byte contract changed")
	}
	// The original exact source-data and width oracles are intentionally unchanged.
	loadMintedDataOracle(t)
	if _, err := newResponseRuleSnapshot([]responseMintRule{{ID: "caller-long", Expression: `(tok_[A-Z]{1000})`}}); err == nil {
		t.Fatal("a custom grammar gained the trusted service carry")
	}
	var data struct {
		Maximum int `json:"maximum_scan_window_bytes"`
		Rules   []struct {
			mintedRuleDataOracle
			Upstream string `json:"upstream_regex"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(responseServiceMintedData), &data); err != nil || len(data.Rules) != 21 || data.Maximum != 1021 {
		t.Fatal("service source subset identity changed")
	}
	seen := make(map[string]bool)
	maximum := 170
	for _, rule := range data.Rules {
		if seen[rule.ID] {
			t.Fatal("duplicate source family")
		}
		seen[rule.ID] = true
		ast, err := syntax.Parse(rule.Regex, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		capture := mintedDataCapture(ast, 1)
		if capture == nil || rule.Capture != 1 || mintedDataMaximum(t, capture) != rule.SecretMax || mintedDataMaximum(t, ast) != rule.MatchMax || rule.WindowMax != rule.MatchMax+1 || rule.LeftContext != 1 || rule.NetworkEntropy != "do-not-suppress-credential-shaped-values" {
			t.Fatalf("source capture/finite bound changed: %s", rule.ID)
		}
		derived := "(" + rule.Upstream + ")"
		if rule.ID == "npm-access-token" {
			derived = strings.ReplaceAll(strings.ReplaceAll(rule.Upstream, "(?i)", ""), "npm_[a-z0-9]", "(?:npm_|NPM_|npM_|NPm_|nPm_|NpM_|nPM_|Npm_)[a-zA-Z0-9]")
		}
		if rule.Regex != derived {
			t.Fatal("source language was shortened or widened")
		}
		maximum = max(maximum, rule.WindowMax)
	}
	if maximum != 1021 {
		t.Fatal("carry is not derived from the source maximum")
	}
	for _, fixture := range serviceMintFixtures() {
		if !seen[fixture.id] {
			t.Fatal("missing finite source family")
		}
	}
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil || len(rules.rules) != 24 || rules.window != 1021 {
		t.Fatal("not one complete pinned snapshot")
	}
	for _, changed := range []string{responseServiceMintedData + "\n", strings.Replace(responseServiceMintedData, "{50,1000}", "{50,999}", 1), strings.Replace(responseServiceMintedData, "do-not-suppress-credential-shaped-values", "entropy-gate", 1)} {
		if _, err := appendPinnedServiceResponseRules(model, changed); err == nil {
			t.Fatal("service source pin accepted edited rule data")
		}
	}
	digest := sha256.Sum256([]byte(responseMintedData))
	if hex.EncodeToString(digest[:]) != "936f8f8063899ba607ebb2c415af2e46f6df0a5aeec9f4a1c1546f432531f902" {
		t.Fatal("original model data bytes changed")
	}
}

func runServiceMintStream(t *testing.T, rules *responseRuleSnapshot, fixture serviceMintFixture, input string, split int, wantMint bool) {
	t.Helper()
	var current atomic.Pointer[responseEchoSnapshot]
	current.Store(responseTestEchoes(t))
	calls := 0
	standin := api.Secret(strings.Repeat("Z", len(fixture.value)))
	reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: []string{input[:split], input[split:]}}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, rules, func(_ context.Context, value api.Secret, id string) (api.Secret, error) {
		calls++
		if id != fixture.id || value != api.Secret(fixture.value) {
			return "", errors.New("wrong source capture")
		}
		current.Store(responseTestEchoes(t, responseEchoPair{Value: value, Sentinel: standin}))
		return standin, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	want := input
	if wantMint {
		want = strings.ReplaceAll(input, fixture.value, string(standin))
	}
	if err != nil || string(got) != want || calls != map[bool]int{true: 1, false: 0}[wantMint] {
		t.Fatalf("%s split%d lost source capture/publication: calls%d error%v", fixture.id, split, calls, err)
	}
	if len(reader.pending()) > 2*reader.maxEcho+reader.patternWindow()+responseReadBuffer || cap(reader.buffer) > 2*reader.maxEcho+reader.patternWindow()+responseReadBuffer {
		t.Fatal("finite response carry exceeded its derived budget")
	}
}

func TestServiceMintFamiliesPreserveSplitsCaptureAndPublishedEcho(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range serviceMintFixtures() {
		t.Run(fixture.id, func(t *testing.T) {
			input := "\"" + fixture.value + "\";later=" + fixture.value + ":"
			cuts := []int{0, 1, 2, len(fixture.value) / 2, len(fixture.value), len(fixture.value) + 1, len(input) - 1, len(input)}
			for _, n := range []int{169, 170, 171, 316, 318, 1000, 1019, 1020, 1021} {
				if n < len(input) {
					cuts = append(cuts, n)
				}
			}
			slices.Sort(cuts)
			cuts = slices.Compact(cuts)
			for _, split := range cuts {
				runServiceMintStream(t, rules, fixture, input, split, true)
			}
		})
	}
}

func TestServiceMintLongFiniteFamiliesKeepBoundaryNegatives(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range serviceMintFixtures() {
		if fixture.id != "pypi-upload-token" && !strings.HasSuffix(fixture.id, "-routable") {
			continue
		}
		t.Run(fixture.id, func(t *testing.T) {
			// Mutate the prefix so no shorter family may legitimately match.
			bad := "!" + fixture.value[1:]
			for _, split := range []int{1, 169, 170, 171, len(bad) - 1, len(bad)} {
				runServiceMintStream(t, rules, fixture, bad, split, false)
			}
			if fixture.id == "pypi-upload-token" {
				minimum := serviceMintFixture{fixture.id, "pypi-AgEIcHlwaS5vcmc" + strings.Repeat("A", 50)}
				for _, split := range []int{19, 20, len(minimum.value) - 1, len(minimum.value)} {
					runServiceMintStream(t, rules, minimum, minimum.value, split, true)
				}
				short := minimum.value[:len(minimum.value)-1] + "!"
				for _, split := range []int{20, len(short) - 1, len(short)} {
					runServiceMintStream(t, rules, minimum, short, split, false)
				}
			} else {
				// Scope this boundary oracle to the exact longer source rule.
				// The combined snapshot also legitimately recognises a legacy
				// glpat prefix; that must not make the long rule's boundary pass.
				var isolated *responseRuleSnapshot
				for _, program := range rules.rules {
					if program.id == fixture.id {
						isolated = &responseRuleSnapshot{rules: []responseRuleProgram{program}, first: program.first, window: rules.window}
					}
				}
				if isolated == nil {
					t.Fatal("missing long rule")
				}
				bad = fixture.value + "x"
				for _, split := range []int{169, 170, len(fixture.value), len(bad)} {
					runServiceMintStream(t, isolated, fixture, bad, split, false)
				}
			}
		})
	}
}

func TestServiceMintASCIICaseAlternativesAndCancellation(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"npm_" + strings.Repeat("a", 36), "NPM_" + strings.Repeat("Z", 36), "NpM_" + strings.Repeat("1", 36)} {
		runServiceMintStream(t, rules, serviceMintFixture{"npm-access-token", token}, token, len(token), true)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader, err := newResponseCredentialReader(ctx, io.NopCloser(strings.NewReader(serviceMintFixtures()[len(serviceMintFixtures())-1].value)), func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, rules, func(context.Context, api.Secret, string) (api.Secret, error) {
		t.Fatal("cancelled stream registered a credential")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled service stream emitted source")
	}
	if _, err := newResponseRuleSnapshotWithinWindow([]responseMintRule{{ID: "unicode-fold", Expression: `(?i:(key))`}}, responseServicePatternWindow); err == nil {
		t.Fatal("Unicode folds entered the finite ASCII scanner")
	}
}

// The source has no retained64MiB payload. This runs the production combined
// snapshot, retaining the original benign-stream carry threshold unchanged.
func TestServiceMintCombinedSnapshotMemoryDoesNotScaleWithBody(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	source := &responseObservedReader{remaining: 64 << 20}
	reader, err := newResponseCredentialReader(t.Context(), source, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, rules, func(context.Context, api.Secret, string) (api.Secret, error) {
		t.Fatal("benign stream minted a credential")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	buffer := make([]byte, responseReadBuffer)
	total, peak := 0, 0
	for {
		n, err := reader.Read(buffer)
		total += n
		peak = max(peak, cap(reader.buffer))
		if len(reader.pending()) > responsePatternWindow+responseReadBuffer || cap(reader.buffer) > responsePatternWindow+responseReadBuffer {
			t.Fatal("combined benign reader exceeded the original streaming carry threshold")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if total != 64<<20 {
		t.Fatal("combined reader truncated a streaming body")
	}
	t.Logf("combined24-family body=%d peak_carry_capacity=%d total_alloc_delta=%d", total, peak, after.TotalAlloc-before.TotalAlloc)
}

func TestServiceMintCombinedMaximumFamiliesKeepOneByteDemandAndCancellation(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range serviceMintFixtures() {
		if len(fixture.value) < 170 {
			continue
		}
		t.Run(fixture.id, func(t *testing.T) {
			var pieces []string
			input := "\"" + fixture.value + "\";"
			for i := range len(input) {
				pieces = append(pieces, input[i:i+1])
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var current atomic.Pointer[responseEchoSnapshot]
			current.Store(responseTestEchoes(t))
			standin := api.Secret(strings.Repeat("Z", len(fixture.value)))
			calls := 0
			reader, err := newResponseCredentialReader(ctx, &responsePieceReader{pieces: pieces}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, rules, func(_ context.Context, value api.Secret, id string) (api.Secret, error) {
				calls++
				if value != api.Secret(fixture.value) || id != fixture.id {
					return "", errors.New("wrong one-byte candidate")
				}
				current.Store(responseTestEchoes(t, responseEchoPair{Value: value, Sentinel: standin}))
				return standin, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			var output strings.Builder
			one := make([]byte, 1)
			started := time.Now()
			peak := 0
			for {
				n, err := reader.Read(one)
				if n > 0 {
					output.Write(one[:n])
				}
				peak = max(peak, cap(reader.buffer))
				if len(reader.pending()) > reader.maxEcho+reader.patternWindow()+responseReadBuffer {
					t.Fatal("long finite carry exceeded exact bound")
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 || output.String() != "\""+string(standin)+"\";" {
				t.Fatal("one-byte long family lost its same source capture")
			}
			t.Logf("actual_combined family=%s source_bytes=%d peak_carry_capacity=%d elapsed=%s", fixture.id, len(input), peak, time.Since(started))
		})
	}
}

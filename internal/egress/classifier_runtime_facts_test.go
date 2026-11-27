package egress

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func TestClassifierFactsMissingWitnessesStayUnknownAndOptionsStayDisabled(t *testing.T) {
	server, _ := judgeCapabilityFixture(t)
	actual, domain := server.collectClassifierFacts(t.Context(), Request{Host: "docs.example.com", Port: 443}, api.Policy{})
	if domain != "example.com" || actual.PreviouslyReached.Known || actual.PreviouslyReached.Value || actual.DomainAge.Status != classify.FactDisabled || actual.Popularity.Status != classify.FactDisabled {
		t.Fatal("missing observations became false/old/popular facts")
	}
	server.synth.reserveUnknown("accepted.example.com")
	server.synth.acceptUnknown(server.synth.unknownID("accepted.example.com"))
	actual, _ = server.collectClassifierFacts(t.Context(), Request{Host: "docs.example.com", Port: 443}, api.Policy{})
	if !actual.PreviouslyReached.Known || !actual.PreviouslyReached.Value {
		t.Fatal("actual accepted destination witness was ignored")
	}
}

func TestClassifierPopularityUsesOnlyConfiguredExactRankWitness(t *testing.T) {
	data := []byte("rank,domain\n1,other.example\n12,example.com\n")
	if actual := readClassifierRankV1(t.Context(), bytes.NewReader(data), int64(len(data)), "example.com"); actual.Status != classify.FactKnown || actual.Rank != 12 {
		t.Fatal("configured rank witness changed")
	}
	if actual := readClassifierRankV1(t.Context(), bytes.NewReader(data), int64(len(data)), "missing.example"); actual.Status != classify.FactUnknown || actual.Rank != 0 {
		t.Fatal("missing rank became known")
	}
	data = []byte("1,example.com\n2,example.com\n")
	if actual := readClassifierRankV1(t.Context(), bytes.NewReader(data), int64(len(data)), "example.com"); actual.Status != classify.FactUnknown {
		t.Fatal("conflicting popularity rows supplied a fact")
	}
}

func TestClassifierRDAPUsesAuthoritativeBootstrapCachesAndRefusesInventedOldAge(t *testing.T) {
	server, _ := judgeCapabilityFixture(t)
	server.classifier = newClassifierRuntime(server)
	t.Cleanup(server.classifier.close)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	calls := 0
	server.classifier.facts.testFetch = func(_ context.Context, target string, _ int64) ([]byte, error) {
		calls++
		if target == "https://data.iana.org/rdap/dns.json" {
			return []byte(`{"services":[[["com"],["https://registry.example/rdap/"]]]}`), nil
		}
		if target != "https://registry.example/rdap/domain/example.com" {
			t.Fatal("bootstrap changed the exact registry target")
		}
		return []byte(`{"objectClassName":"domain","ldhName":"EXAMPLE.COM","events":[{"eventAction":"registration","eventDate":"2026-11-10T00:00:00Z"}]}`), nil
	}
	enabled := true
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{RDAP: &enabled}}}
	for range 2 {
		actual, _ := server.collectClassifierFacts(judgeSourceContext(t, server), Request{Host: "docs.example.com", Port: 443}, policy)
		if actual.DomainAge.Status != classify.FactKnown || actual.DomainAge.Days != 10 {
			t.Fatal("registration event did not supply its actual age")
		}
	}
	if calls != 2 {
		t.Fatal("same-day facts repeated RDAP network lookups")
	}
	for _, raw := range []string{
		`{"objectClassName":"domain","ldhName":"other.com","events":[{"eventAction":"registration","eventDate":"2020-01-01T00:00:00Z"}]}`,
		`{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"last changed","eventDate":"2020-01-01T00:00:00Z"}]}`,
		`{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"malformed"}]}`,
	} {
		if !rdapRegistration([]byte(raw), "example.com").IsZero() {
			t.Fatal("unrelated or malformed event became old-domain evidence")
		}
	}
	if actual := ageAt(now.Add(time.Hour), now); actual.Status != classify.FactUnknown {
		t.Fatal("future registration became an old domain")
	}
}

func TestClassifierRDAPFailureAndRepositoryControlledFactSettingsNeverInventFacts(t *testing.T) {
	server, _ := judgeCapabilityFixture(t)
	server.classifier = newClassifierRuntime(server)
	t.Cleanup(server.classifier.close)
	calls := 0
	server.classifier.facts.testFetch = func(context.Context, string, int64) ([]byte, error) {
		calls++
		return nil, errors.New("controlled lookup failure")
	}
	enabled := true
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{RDAP: &enabled}}}
	actual, _ := server.collectClassifierFacts(judgeSourceContext(t, server), Request{Host: "docs.example.com", Port: 443}, policy)
	if actual.DomainAge.Status != classify.FactUnknown || actual.DomainAge.Days != 0 || calls != 1 {
		t.Fatal("lookup failure was labelled old or skipped real provider")
	}
	policy.Origins = []api.ValueOrigin{{Key: "classifier.rdap", Source: api.SourceRepositoryFile}}
	actual, _ = server.collectClassifierFacts(judgeSourceContext(t, server), Request{Host: "another.example", Port: 443}, policy)
	if actual.DomainAge.Status != classify.FactUnknown || calls != 1 {
		t.Fatal("repository-controlled fact setting made a foreign lookup")
	}
}

func TestClassifierRDAPBootstrapUsesLongestBoundaryAndHTTPSOnly(t *testing.T) {
	services := parseRDAPBootstrap([]byte(`{"services":[[["test"],["https://general.example/"]],[["co.test"],["https://specific.example/"]]]}`))
	if rdapService(services, "x.co.test") != "https://specific.example/" || rdapService(services, "notcotest") == "https://specific.example/" {
		t.Fatal("RDAP bootstrap lost suffix boundaries")
	}
	for _, endpoint := range []string{"http://registry.example/", "https://name:password@registry.example/", "https://registry.example/?query=value", "https://registry.example/#fragment"} {
		raw := `{"services":[[["test"],["` + endpoint + `"]]]}`
		if len(parseRDAPBootstrap([]byte(raw))) != 0 {
			t.Fatal("decorated or cleartext registry endpoint accepted")
		}
	}
	if value := rdapRegistration([]byte(`{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2020-01-01T00:00:00Z"},{"eventAction":"registration","eventDate":"2026-11-19T00:00:00Z"}]}`), "example.com"); !strings.HasPrefix(value.Format(time.RFC3339), "2026-11-19") {
		t.Fatal("later registration was hidden by an older event")
	}
}

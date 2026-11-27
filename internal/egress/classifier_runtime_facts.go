package egress

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

type classifierAgeCache struct {
	fact             classify.AgeFact
	created, expires time.Time
}
type classifierFacts struct {
	popularity       *PopularitySourceV1
	mu               sync.Mutex
	ages             map[string]classifierAgeCache
	bootstrap        map[string]string
	bootstrapUntil   time.Time
	bootstrapLoading chan struct{}
	// Only package tests replace HTTP; production uses the owned HTTPS path.
	testFetch func(context.Context, string, int64) ([]byte, error)
}

func trustedFactSetting(policy api.Policy, key string) bool {
	for _, origin := range policy.Origins {
		if origin.Key == key && origin.Source == api.SourceRepositoryFile {
			return false
		}
	}
	return true
}

// collectClassifierFacts is the sole fact provider. Missing witnesses remain
// unknown. In particular an empty accepted-destination table proves no false
// fact about previously reaching a domain.
func (s *Server) collectClassifierFacts(ctx context.Context, request Request, policy api.Policy) (classify.Facts, string) {
	facts := classify.Facts{DomainAge: classify.AgeFact{Status: classify.FactDisabled}, Popularity: classify.RankFact{Status: classify.FactDisabled}}
	domain, err := publicsuffix.EffectiveTLDPlusOne(request.Host)
	if err != nil {
		domain = ""
	}
	if domain != "" {
		s.mu.RLock()
		table := s.synth
		s.mu.RUnlock()
		if table != nil {
			table.mu.RLock()
			for host, record := range table.byHost {
				if record.announced && (host == domain || strings.HasSuffix(host, "."+domain)) {
					facts.PreviouslyReached = classify.KnownBool{Known: true, Value: true}
					break
				}
			}
			table.mu.RUnlock()
		}
	}
	if enabled := policy.Classifier.RDAP; enabled != nil && *enabled {
		facts.DomainAge.Status = classify.FactUnknown
		if domain != "" && trustedFactSetting(policy, "classifier.rdap") && s.classifier != nil {
			facts.DomainAge = s.classifier.facts.domainAge(ctx, s, domain)
		}
	}
	if source := policy.Classifier.Popularity; source != nil && *source != "" {
		facts.Popularity.Status = classify.FactUnknown
		if domain != "" && trustedFactSetting(policy, "classifier.popularity") && s.classifier != nil {
			facts.Popularity = s.classifier.facts.popularity.rank(ctx, s, *source, domain)
		}
	}
	return facts, domain
}

func (facts *classifierFacts) domainAge(ctx context.Context, s *Server, domain string) classify.AgeFact {
	now := s.now()
	unknown := classify.AgeFact{Status: classify.FactUnknown}
	facts.mu.Lock()
	cached, exists := facts.ages[domain]
	facts.mu.Unlock()
	if exists && now.Before(cached.expires) {
		if cached.fact.Status == classify.FactKnown {
			return ageAt(cached.created, now)
		}
		return cached.fact
	}
	endpoint := facts.rdapEndpoint(ctx, s, domain)
	var created time.Time
	if endpoint != "" {
		base, err := url.Parse(endpoint)
		if err == nil {
			raw, err := facts.fetch(ctx, s, base.JoinPath("domain", domain).String(), 256<<10)
			if err == nil {
				created = rdapRegistration(raw, domain)
			}
		}
	}
	result := unknown
	if !created.IsZero() {
		result = ageAt(created, now)
	}
	facts.mu.Lock()
	if facts.ages == nil {
		facts.ages = make(map[string]classifierAgeCache)
	}
	for name, entry := range facts.ages {
		if !now.Before(entry.expires) {
			delete(facts.ages, name)
		}
	}
	if _, present := facts.ages[domain]; present || len(facts.ages) < MaxSyntheticNames {
		facts.ages[domain] = classifierAgeCache{fact: result, created: created, expires: now.Add(24 * time.Hour)}
	}
	facts.mu.Unlock()
	return result
}

func ageAt(created, now time.Time) classify.AgeFact {
	if created.IsZero() || created.After(now) {
		return classify.AgeFact{Status: classify.FactUnknown}
	}
	days := (now.Unix() - created.Unix()) / 86400
	if days < 0 || days > int64(^uint32(0)) {
		return classify.AgeFact{Status: classify.FactUnknown}
	}
	return classify.AgeFact{Status: classify.FactKnown, Days: uint32(days)}
}

// RFC 9083 section 4.5 supplies registration eventDate. A missing, malformed
// or future registration is unknown, never evidence that the domain is old.
func rdapRegistration(raw []byte, domain string) time.Time {
	var record struct {
		ObjectClass string `json:"objectClassName"`
		Domain      string `json:"ldhName"`
		Events      []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if json.Unmarshal(raw, &record) != nil || record.ObjectClass != "domain" || !strings.EqualFold(strings.TrimSuffix(record.Domain, "."), domain) || len(record.Events) > 128 {
		return time.Time{}
	}
	var latest time.Time
	for _, event := range record.Events {
		if event.Action != "registration" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.Date)
		if err != nil {
			return time.Time{}
		}
		if at.After(latest) {
			latest = at
		}
	}
	return latest
}

func (facts *classifierFacts) rdapEndpoint(ctx context.Context, s *Server, domain string) string {
	now := s.now()
	facts.mu.Lock()
	if now.Before(facts.bootstrapUntil) {
		result := rdapService(facts.bootstrap, domain)
		facts.mu.Unlock()
		return result
	}
	if loading := facts.bootstrapLoading; loading != nil {
		facts.mu.Unlock()
		select {
		case <-ctx.Done():
			return ""
		case <-loading:
		}
		facts.mu.Lock()
		result := rdapService(facts.bootstrap, domain)
		facts.mu.Unlock()
		return result
	}
	loading := make(chan struct{})
	facts.bootstrapLoading = loading
	facts.mu.Unlock()
	// RFC 9224's IANA bootstrap selects the registry. No guest-controlled
	// lookup URL, third-party relay, redirect or credential is accepted.
	raw, err := facts.fetch(ctx, s, "https://data.iana.org/rdap/dns.json", 1<<20)
	var services map[string]string
	if err == nil {
		services = parseRDAPBootstrap(raw)
	}
	facts.mu.Lock()
	facts.bootstrap = services
	facts.bootstrapUntil = now.Add(24 * time.Hour)
	facts.bootstrapLoading = nil
	close(loading)
	result := rdapService(services, domain)
	facts.mu.Unlock()
	return result
}

func parseRDAPBootstrap(raw []byte) map[string]string {
	var bootstrap struct {
		Services [][][]string `json:"services"`
	}
	if json.Unmarshal(raw, &bootstrap) != nil || len(bootstrap.Services) > 4096 {
		return nil
	}
	services := make(map[string]string)
	for _, entry := range bootstrap.Services {
		if len(entry) != 2 || len(entry[0]) > 4096 || len(entry[1]) > 16 {
			return nil
		}
		endpoint := ""
		for _, candidate := range entry[1] {
			parsed, err := url.Parse(candidate)
			if err == nil && validFactURL(parsed) {
				endpoint = parsed.String()
				break
			}
		}
		if endpoint == "" {
			continue
		}
		for _, suffix := range entry[0] {
			canonical, err := NormaliseHost(suffix)
			if err != nil || canonical != suffix {
				return nil
			}
			if previous, found := services[suffix]; found && previous != endpoint {
				return nil
			}
			services[suffix] = endpoint
			if len(services) > 4096 {
				return nil
			}
		}
	}
	return services
}

func rdapService(services map[string]string, domain string) string {
	best, endpoint := "", ""
	for suffix, candidate := range services {
		if (domain == suffix || strings.HasSuffix(domain, "."+suffix)) && len(suffix) > len(best) {
			best, endpoint = suffix, candidate
		}
	}
	return endpoint
}

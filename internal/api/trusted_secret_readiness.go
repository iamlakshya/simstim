package api

// TrustedSecretReadinessQueryV1 contains expected original local role tuples.
// It is a restriction on one fresh protected cohort read, never an arm or fetch.
type TrustedSecretReadinessQueryV1 struct {
	Version    int                           `json:"v"`
	Generation string                        `json:"generation"`
	Uses       map[string]TrustedSecretUseV1 `json:"uses"`
}

// TrustedSecretReadinessV1 is an ephemeral complete names-only observation.
// It is never persisted, cached or used to authorise a fetch. Only a server
// which compared every requested role tuple may construct this observation.
type TrustedSecretReadinessV1 struct {
	Version    int      `json:"v"`
	Generation string   `json:"generation"`
	Unarmed    []string `json:"unarmed"`
}

// ValidFor validates only an observation's closed finite representation.
// Original Source, role equality, actor and physical freshness remain with
// the existing protected cohort producer and its locally captured request.
func (value TrustedSecretReadinessV1) ValidFor(query TrustedSecretReadinessQueryV1) bool {
	if query.Version != 1 || query.Generation == "" || len(query.Uses) == 0 || value.Version != 1 || value.Generation != query.Generation || value.Unarmed == nil || len(value.Unarmed) > len(query.Uses) {
		return false
	}
	previous := ""
	for _, name := range value.Unarmed {
		if name == "" || name <= previous {
			return false
		}
		if _, known := query.Uses[name]; !known {
			return false
		}
		previous = name
	}
	return true
}

func CloneTrustedSecretReadinessQueryV1(query *TrustedSecretReadinessQueryV1) *TrustedSecretReadinessQueryV1 {
	if query == nil {
		return nil
	}
	result := *query
	result.Uses = CloneTrustedSecretUses(query.Uses)
	return &result
}

func CloneTrustedSecretReadinessV1(value *TrustedSecretReadinessV1) *TrustedSecretReadinessV1 {
	if value == nil {
		return nil
	}
	result := *value
	if value.Unarmed != nil {
		result.Unarmed = append([]string{}, value.Unarmed...)
	}
	return &result
}

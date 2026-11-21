package supervisor

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPopularityStartupRejectsUntrustedOrInvalidAdapterBeforeCapture(t *testing.T) {
	path := "/unselected/rank.csv"
	if prepared, err := prepareClassifierPopularityV1(nil, api.Policy{}); err == nil || prepared != nil {
		t.Fatal("missing original startup context selected a file")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if prepared, err := prepareClassifierPopularityV1(ctx, api.Policy{}); err == nil || prepared != nil {
		t.Fatal("cancelled original startup selected a file")
	}
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{Popularity: &path}}}
	policy.Origins = []api.ValueOrigin{{Key: "classifier.popularity", Source: api.SourceRepositoryFile}}
	if prepared, err := prepareClassifierPopularityV1(t.Context(), policy); err != nil || prepared != nil {
		t.Fatal("repository-controlled popularity selection became a source", err)
	}
	model := "model"
	policy.Origins = nil
	policy.Classifier.Judge = &model
	if prepared, err := prepareClassifierPopularityV1(t.Context(), policy); err != nil || prepared != nil {
		t.Fatal("invalid model adapter selected optional file input", err)
	}
}

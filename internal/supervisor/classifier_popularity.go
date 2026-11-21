package supervisor

import (
	"context"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The original startup factory selects and opens this configured host file
// before any Egress process exists. Control DTOs and hot policy replacement
// have no path-opening route. Failure is an unknown optional fact.
func prepareClassifierPopularityV1(ctx context.Context, policy api.Policy) (*egress.PopularityPreparedV1, error) {
	if ctx == nil {
		return nil, &proto.Error{Code: "classifier_popularity", Message: "popularity preparation has no original startup context", Hint: "Start the sandbox through its original supervisor."}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	selected := policy.Classifier.Popularity
	if selected == nil || *selected == "" {
		return nil, nil
	}
	if !egress.ValidPopularitySelectionV1(*selected) || egress.CheckClassifierConfigurationV1(policy) != nil {
		return nil, nil
	}
	for _, origin := range policy.Origins {
		if origin.Key == "classifier.popularity" && origin.Source == api.SourceRepositoryFile {
			return nil, nil
		}
	}
	absolute, err := filepath.Abs(*selected)
	if err != nil {
		return nil, nil
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, nil
	}
	file, err := files.OpenRegular(root, filepath.Base(absolute))
	_ = root.Close()
	if err != nil {
		return nil, nil
	}
	prepared, err := egress.CapturePopularityV1(ctx, file, *selected)
	if ctx.Err() != nil {
		if prepared != nil {
			_ = prepared.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, nil
	}
	return prepared, nil
}

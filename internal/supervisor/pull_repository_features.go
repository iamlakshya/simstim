package supervisor

import "github.com/iamlakshya/simstim/internal/api"

func init() { Features = append(Features, api.FeaturePullRepositories) }

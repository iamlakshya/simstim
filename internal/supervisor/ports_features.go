package supervisor

import "github.com/iamlakshya/simstim/internal/api"

func init() { Features = append(Features, api.FeatureSandboxPortsV1, api.FeatureReversePortsV1) }

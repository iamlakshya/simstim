package host

import "github.com/iamlakshya/simstim/internal/api"

func init(){if EventsDiscoverySupported(){Features=append(Features,api.FeatureEvents)}}

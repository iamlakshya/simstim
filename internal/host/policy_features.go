package host

import "github.com/iamlakshya/simstim/internal/api"

func init() {
	Features = append(Features, api.FeaturePolicyLockdownV1, api.FeaturePolicyLiveV1, "policy", api.FeatureNetworkHolds, api.FeatureNetworkPublicOnly, api.FeatureHTTPMethods, api.FeatureModelTools, api.FeaturePolicyPush, api.FeaturePolicyLogCursor)
}

package api

// DoorPush opens only one repository's session branches over observed HTTPS.
const DoorPush Door = "push"

// FeaturePolicyPush prevents an older helper from dropping repository push
// authority or treating its target as an ordinary network path allowance.
const FeaturePolicyPush = "policy-push-v1"

// PushModePublic records explicit permission to publish to a public repository.
// The empty mode keeps its existing identity and records no acknowledgement.
const PushModePublic = "public"

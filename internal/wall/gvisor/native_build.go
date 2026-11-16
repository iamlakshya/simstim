package gvisor

// NativePatchSHA256V1 is the digest of third_party/gvisor/hostguard.patch.
// The build route derives its supported eight-hex suffix from these actual
// source bytes. Signed artefact digests still come from the resulting build.
const NativePatchSHA256V1 = "dea09068c65f15216a7fd5bfa5aae4c9e73c9dfb0f46e37224ae7d9c455bb23a"

// NativeBuildVersionV1 identifies the one supported patched companion build.
// An upstream runsc or a different distribution's suffix supplies no memory
// privacy or single-owner file translation contract.
const NativeBuildVersionV1 = "release-20261102.0-dea09068"

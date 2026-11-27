package gvisor

// NativePatchSHA256V1 is the digest of third_party/gvisor/hostguard.patch.
// The build route derives its supported eight-hex suffix from these actual
// source bytes. Signed artefact digests still come from the resulting build.
const NativePatchSHA256V1 = "3d19c2f9e9bc1f3b5dce4370f201d558514efe4aede742d4ee1eabc5bf10920e"

// NativeBuildVersionV1 identifies the one supported patched companion build.
// An upstream runsc or a different distribution's suffix supplies no memory
// privacy or single-owner file translation contract.
const NativeBuildVersionV1 = "release-20261102.0-3d19c2f9"

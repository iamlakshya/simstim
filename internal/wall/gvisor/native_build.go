package gvisor

// NativePatchSHA256V1 is the digest of third_party/gvisor/hostguard.patch.
// The build route derives its supported eight-hex suffix from these actual
// source bytes. Signed artefact digests still come from the resulting build.
const NativePatchSHA256V1 = "0c64744315f5f536ad7ccc082687f4539765a3a21ce6cd42c4ec84e7784ec7f2"

// NativeBuildVersionV1 identifies the one supported patched companion build.
// An upstream runsc or a different distribution's suffix supplies no memory
// privacy or single-owner file translation contract.
const NativeBuildVersionV1 = "release-20261102.0-0c647443"

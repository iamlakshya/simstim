# Offline image verifier

This separate module keeps `sigstore-go` v1.4.0 on its required Go 1.27
toolchain. The client and host module keeps its existing Go 1.26 toolchain.
The exact Go 1.27.4 archives are in `toolchain.lock`, taken from
[Go's release metadata](https://go.dev/dl/?mode=json).

The release builds this module with its separately checked compiler, with
automatic toolchain downloads disabled. It carries one verifier for each
supported client platform: Linux and macOS, on amd64 and arm64. The release
manifest records each executable's actual size and SHA-256.

The only command is `--image-signature-v1`. It reads one bounded request on
stdin, checks the bundle offline, then writes one response on stdout. The
shared protocol is `internal/imageverify`. A successful response names its
role, release version and the SHA-256 of the complete original request. That
request includes a fresh nonce, the image digest, exact certificate policy,
trusted root and signature bytes. A failed check writes no response.

The verifier uses no TUF client or live trusted root. It requires one
transparency log entry and one observed timestamp. New bundles must bind the
artifact digest. Legacy signatures must both verify the original layer bytes
and name that same image digest in their signed simple-signing claims.

The client accepts a response only from its original checked child. It keeps
the private executable stage until the child joins. Linux executes a sealed
descriptor. macOS executes the private stage by name, with its original
directory and file checked before and after the child. These laptop checks
do not defend another process running as the laptop user.

`testdata/signature-v0.3.json` and `testdata/trusted-root.json` are unchanged
public fixtures from sigstore-go v1.4.0. They use a local test issuer and
test log. See `NOTICE` for attribution.
The `cosign-oci-v0.3.json` bundle and its `cosign-trusted-root.json` come from
cosign v3.1.1's recorded OCI verification test. They check the normal DSSE
image-digest path with public Sigstore trust material.

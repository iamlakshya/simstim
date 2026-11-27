# Refresh the image signature trust root

Refresh `internal/supply/trusted_root.json` in each client release change,
before creating its signed tag. Commit the reviewed bytes with the client.
Ordinary builds use that file unchanged. The installed verifier reads no live
TUF repository and refreshes no key while checking an image.

Use a checked cosign v3.1.1 binary for this procedure. Its `initialize`
command starts from its embedded public Sigstore TUF root. It authenticates
the current metadata and target. Do not select the staging mirror or replace
that initial root during a normal release refresh.

1. Create a fresh private cache used only for this refresh:

   ```sh
   trust_refresh_dir=$(mktemp -d)
   TUF_ROOT="$trust_refresh_dir" cosign initialize
   test -s "$trust_refresh_dir/targets/trusted_root.json"
   shasum -a 256 "$trust_refresh_dir/targets/trusted_root.json"
   ```

   Require the authenticated `trusted_root.json` target. A fallback to older
   individual targets does not supply this release input. The temporary cache
   also holds the signed metadata needed to review the target's provenance.

2. Review the target against the public
   [Sigstore root-signing repository](https://github.com/sigstore/root-signing).
   Review added and removed Fulcio, Rekor, certificate transparency and
   timestamp keys, including their validity intervals. Record the target's
   SHA-256, metadata versions and upstream change in the release review.
   Preserve the trust material needed by the recorded verification fixtures.

3. Copy the authenticated target without reformatting it:

   ```sh
   cp "$trust_refresh_dir/targets/trusted_root.json" internal/supply/trusted_root.json
   git diff -- internal/supply/trusted_root.json
   ```

   Update the source and retrieval date in `NOTICE`. Keep the Sigstore
   copyright and Apache-2.0 entries in `REUSE.toml`. If the target is unchanged,
   record that the release checked the same SHA-256 again.

4. Run the offline verifier's tests with the pinned Go toolchain:

   ```sh
   (
       cd tools/imageverify
       go test -tags=http2legacy -mod=readonly ./...
   )
   just test lint
   just docs-check
   ```

   Both the root module and `tools/imageverify` use the pinned Go 1.27.4
   toolchain from `go.mod`, with `sigstore-go` v1.4.0 in `tools/imageverify`.
   Run these checks in CI for the release change. A failing recorded signature
   stops the refresh; do not disable identity, log or timestamp checks.

The release carries the root through the client binary. The client binds its
accepted signature cache to the exact root bytes, identity and issuer. A root
change therefore checks the existing image pin again at the next client-issued
start. It does not change that image digest. A signature using a newer key
requires a newer client root or the user's explicit laptop `trusted_root`
file.

The refresh command and cache layout are defined by cosign v3.1.1's
[initialisation source](https://github.com/sigstore/cosign/blob/v3.1.1/cmd/cosign/cli/initialize/init.go)
and [recorded cache tests](https://github.com/sigstore/cosign/blob/v3.1.1/cmd/cosign/cli/initialize/init_test.go).
The verifier uses Sigstore's
[offline verification API](https://github.com/sigstore/sigstore-go/blob/v1.4.0/docs/verification.md).

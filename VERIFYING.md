# Verify a release

Download the archive or Linux package for your platform and `checksums.txt` from this repository's Releases.
Verify the checksums manifest itself before trusting its contents:

```sh
gh attestation verify checksums.txt --repo hyperoptimizer/cli --signer-workflow hyperoptimizer/cli/.github/workflows/release.yml
# Replace the archive filename with the version and platform you downloaded.
gh attestation verify hpo_0.1.0_linux_amd64.tar.gz --repo hyperoptimizer/cli --signer-workflow hyperoptimizer/cli/.github/workflows/release.yml
sha256sum --ignore-missing -c checksums.txt
```

On macOS use `shasum -a 256` and compare the relevant checksum. On Windows use
`Get-FileHash -Algorithm SHA256`. Provenance links an asset to its source commit
and build workflow; inspect the verified commit and workflow as part of your review.

# Rebuild

For releases using GoReleaser, check out the exact release tag and install the exact
Go version in `go.mod`, GoReleaser version in `.goreleaser-version`, and Python 3.10+,
then run (example version):

```sh
git clone https://github.com/hyperoptimizer/cli.git
cd cli
git checkout v0.2.0
go mod verify
sh package.sh 0.2.0
```

The packaging helper runs GoReleaser with publishing disabled. Public assets are
staged under `dist/release/`; other files in `dist/` are internal build metadata.
Builds strip absolute paths and VCS metadata, disable CGO, and normalize archive
and package timestamps to the source commit. The official build runs on Ubuntu;
match that environment and the pinned GoReleaser/Go versions when comparing bytes.
Both CI and release jobs compare all staged assets from two builds with a cleared
Go cache. This demonstrates repeatability on the build host; independent rebuilds
provide additional evidence.

`dependencies.json` records the complete resolved Go module graph, including build
and test tooling; it is a dependency inventory, not a binary-only SBOM.
`checksums.txt` covers the archives, Linux packages, dependency inventory, and
generated `hyperoptimizer.rb` cask. Every staged file, including the manifest,
receives a GitHub provenance attestation before the draft release is created.

The Homebrew update workflow verifies attestations for both the generated cask and
the manifest, then checks the cask's checksum before proposing it for the tap.
Homebrew itself checks archive SHA-256 values during installation; it does not run
GitHub attestation verification on the user's behalf.

The existing v0.1.0 release uses the previous Python packager. Rebuild that release
with the scripts and instructions from its own tag; GoReleaser does not reproduce
those older archive bytes. The v0.2.0 commands above are illustrative; substitute
the release tag you are verifying.

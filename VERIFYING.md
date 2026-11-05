# Verify a release

Download the archive for your platform and `checksums.txt` from this repository's Releases.
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

Check out the exact release tag, install the exact Go version in go.mod and Python 3,
then run (example version):

```sh
git clone https://github.com/hyperoptimizer/cli.git
cd cli
git checkout v0.1.0
go mod verify
sh package.sh 0.1.0
```

The packaging script uses the commit time, strips absolute build paths and VCS
metadata, disables CGO, and normalizes archive timestamps, ownership, and ordering.
Do not override SOURCE_DATE_EPOCH or HPO_BUILD_PLATFORMS when reproducing official assets.
The official build runs on Ubuntu; match that environment and Python version when
comparing compressed archive bytes. Compiler version must match exactly.
Both CI and release jobs compare all artifacts from two builds with a cleared Go cache.
This demonstrates repeatability on the build host; independent rebuilds provide additional evidence.

`dependencies.json` records the complete resolved Go module graph, including build
and test tooling; it is a dependency inventory, not a binary-only SBOM.

#!/usr/bin/env python3
"""Run pinned GoReleaser locally and stage only public, attestable release assets."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess


def output(command: list[str]) -> str:
    return subprocess.run(command, check=True, capture_output=True, text=True).stdout.strip()


def stage_assets(dist: Path) -> None:
    """Exclude internal metadata and intermediate binaries from releases."""
    release = dist / "release"
    release.mkdir()
    archives = sorted(dist.glob("hpo_*.tar.gz")) + sorted(dist.glob("hpo_*.zip"))
    packages = sorted(dist.glob("hyperoptimizer_*.deb")) + sorted(dist.glob("hyperoptimizer_*.rpm"))
    casks = list(dist.rglob("hyperoptimizer.rb"))
    if len(archives) != 6 or len(packages) != 4 or len(casks) != 1:
        raise SystemExit("Expected six archives, four Linux packages, and one generated Homebrew cask")
    for source in [*archives, *packages, *casks]:
        shutil.copyfile(source, release / source.name)
    # GoReleaser 2.20 puts preflight between binary and completion artifacts.
    # Group the artifact stanzas before hooks for brew style; preflight still
    # executes before installation. Normalize before checksumming and attesting.
    cask = release / "hyperoptimizer.rb"
    cask_source = cask.read_text(encoding="utf-8")
    preflight = re.search(r"\n  preflight_steps do\n.*?\n  end\n", cask_source, re.S)
    if preflight is not None:
        cask_source = cask_source.replace(preflight[0], "")
        cask_source = cask_source.replace("\n  zap ", preflight[0] + "\n  zap ")
    cask_source = cask_source.replace("\n\n  generate_completions_from_executable", "\n  generate_completions_from_executable")
    cask.write_text(cask_source, encoding="utf-8")

    # Complete Go module inventory, including tools and test dependencies.
    raw = output(["go", "list", "-mod=readonly", "-m", "-json", "all"])
    modules = []
    decoder = json.JSONDecoder()
    while raw.strip():
        item, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        modules.append({key: item[key] for key in ("Path", "Version", "Sum", "GoModSum") if key in item})
    (release / "dependencies.json").write_text(
        json.dumps({"format": "go-module-inventory-v1", "modules": sorted(modules, key=lambda item: item["Path"])}, indent=2) + "\n",
        encoding="utf-8",
    )
    # Extend GoReleaser's checksums to include the cask and module inventory too.
    (release / "checksums.txt").write_text(
        "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in sorted(release.iterdir())),
        encoding="utf-8",
    )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", nargs="?", help="Version without v; must match the tag at HEAD")
    parser.add_argument("--snapshot", action="store_true", help="Build unpublished development packages")
    args = parser.parse_args()
    if (args.snapshot and args.version is not None) or (not args.snapshot and args.version is None):
        parser.error("use VERSION for a tagged release, or --snapshot for a development build")

    root = Path(__file__).resolve().parent.parent
    os.chdir(root)
    go_match = re.search(r"^go (\S+)", (root / "go.mod").read_text(encoding="utf-8"), re.M)
    assert go_match is not None
    expected = "go" + go_match[1]
    actual = output(["go", "env", "GOVERSION"])
    if actual != expected:
        raise SystemExit(f"Use the pinned toolchain {expected}; found {actual}")
    expected_releaser = (root / ".goreleaser-version").read_text(encoding="utf-8").strip()
    if f"GitVersion:    {expected_releaser}" not in output(["goreleaser", "--version"]):
        raise SystemExit(f"Use GoReleaser {expected_releaser}")

    if not args.snapshot:
        if re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", args.version) is None:
            parser.error("VERSION must be a semantic version without the v prefix")
        if output(["git", "describe", "--tags", "--exact-match", "HEAD"]) != "v" + args.version:
            raise SystemExit("VERSION must match the release tag at HEAD")
        if output(["git", "status", "--porcelain"]) != "":
            raise SystemExit("Release builds require a clean checkout")

    # Never publish here: compare builds and attest assets before creating the draft.
    command = ["goreleaser", "release", "--clean", "--skip=publish", "--release-notes", "RELEASE_NOTES.md"]
    if args.snapshot:
        command.append("--snapshot")
    subprocess.run(command, check=True, env={**os.environ, "GOTOOLCHAIN": "local", "GOFLAGS": ""})
    stage_assets(root / "dist")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Smoke-test the actual archives and package payloads produced by package.sh."""

import hashlib
import io
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


def check() -> None:
    release = Path(__file__).resolve().parent.parent / "dist" / "release"
    manifest = dict(
        line.split(maxsplit=1)[::-1]
        for line in (release / "checksums.txt").read_text(encoding="utf-8").splitlines()
    )
    assert set(manifest) == {path.name for path in release.iterdir()} - {"checksums.txt"}
    for name, checksum in manifest.items():
        assert hashlib.sha256((release / name).read_bytes()).hexdigest() == checksum, name

    native_os = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    native_arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    cask = (release / "hyperoptimizer.rb").read_text(encoding="utf-8")
    linux_binaries: dict[str, bytes] = {}
    archives = sorted(release.glob("hpo_*.tar.gz")) + sorted(release.glob("hpo_*.zip"))
    assert len(archives) == 6
    for path in archives:
        if path.suffix == ".zip":
            with zipfile.ZipFile(path) as archive:
                assert set(archive.namelist()) == {"hpo.exe", "hyperoptimizer.exe", "README.md", "LICENSE"}
                assert archive.read("hpo.exe") == archive.read("hyperoptimizer.exe")
            continue
        with tarfile.open(path) as archive:
            assert set(archive.getnames()) == {"hpo", "hyperoptimizer", "README.md", "LICENSE"}
            hpo = archive.extractfile("hpo")
            alias = archive.extractfile("hyperoptimizer")
            assert hpo is not None and alias is not None
            binary = hpo.read()
            assert binary == alias.read(), path.name
            assert archive.getmember("hpo").mode & 0o111
        assert manifest[path.name] in cask, path.name
        if "_linux_" in path.name:
            linux_binaries[path.name.removesuffix(".tar.gz").rsplit("_", 1)[1]] = binary
        if path.name.endswith(f"_{native_os}_{native_arch}.tar.gz"):
            with tempfile.TemporaryDirectory() as temporary:
                executable = Path(temporary) / "hpo"
                executable.write_bytes(binary)
                executable.chmod(0o755)
                version = path.name.removeprefix("hpo_").removesuffix(f"_{native_os}_{native_arch}.tar.gz")
                assert subprocess.run([str(executable), "version"], check=True, capture_output=True, text=True).stdout == f"hpo {version}\n"
                for shell in ("bash", "zsh", "fish"):
                    assert subprocess.run([str(executable), "completion", shell], check=True, capture_output=True).stdout

    # libarchive reads RPM payloads; Debian packages wrap their tar payload in ar.
    bsdtar = shutil.which("bsdtar") if platform.system() != "Darwin" else "/usr/bin/tar"
    if bsdtar is None:
        raise SystemExit("Install libarchive-tools (bsdtar) to check package payloads")
    packages = sorted(release.glob("*.deb")) + sorted(release.glob("*.rpm"))
    assert len(packages) == 4
    for path in packages:
        with tempfile.TemporaryDirectory() as temporary:
            if path.suffix == ".deb":
                members = subprocess.run(["ar", "t", str(path)], check=True, capture_output=True, text=True).stdout.splitlines()
                payload = next(name for name in members if name.startswith("data.tar"))
                data = subprocess.run(["ar", "p", str(path), payload], check=True, capture_output=True).stdout
                with tarfile.open(fileobj=io.BytesIO(data)) as archive:
                    # These locally generated packages intentionally link to /usr/bin/hpo.
                    archive.extractall(temporary, filter="tar")
            else:
                subprocess.run([bsdtar, "-xf", str(path), "-C", temporary], check=True)
            root = Path(temporary)
            arch = path.stem.rsplit("_", 1)[1]
            assert (root / "usr/bin/hpo").read_bytes() == linux_binaries[arch], path.name
            assert (root / "usr/bin/hyperoptimizer").is_symlink(), path.name
            # RPM encodes symlink mode as zero. Linux ignores it, but macOS
            # needs read permission to inspect the extracted link target.
            if path.suffix == ".rpm" and platform.system() == "Darwin":
                (root / "usr/bin/hyperoptimizer").chmod(0o777, follow_symlinks=False)
            assert (root / "usr/bin/hyperoptimizer").readlink() == Path("/usr/bin/hpo"), path.name
            assert (root / "usr/share/licenses/hyperoptimizer/LICENSE").is_file(), path.name
    print("Verified all checksums, six archives, four package payloads, aliases, and native CLI/completions")


if __name__ == "__main__":
    check()

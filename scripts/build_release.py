#!/usr/bin/env python3
"""Package the built plugin for the CPA plugin store.

Release layout (see the CPA plugin development docs):

    <pluginID>_<version>_<goos>_<goarch>.zip   # the library sits at the zip root
    checksums.txt                              # "<sha256>  <zip name>"

Usage:
    python scripts/build_release.py                      # current platform
    python scripts/build_release.py --binary dist/anyrouter.so --goos linux --goarch amd64
"""

from __future__ import annotations

import argparse
import hashlib
import pathlib
import re
import subprocess
import sys
import zipfile

PLUGIN_ID = "anyrouter"
EXTENSIONS = {"windows": "dll", "darwin": "dylib", "linux": "so", "freebsd": "so"}


def plugin_version(root: pathlib.Path) -> str:
    match = re.search(r'pluginVersion\s*=\s*"([^"]+)"', (root / "main.go").read_text(encoding="utf-8"))
    if not match:
        raise SystemExit("cannot find pluginVersion in main.go")
    return match.group(1)


def go_env(key: str) -> str:
    return subprocess.run(["go", "env", key], capture_output=True, text=True, check=True).stdout.strip()


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    root = pathlib.Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser()
    parser.add_argument("--goos", default=None)
    parser.add_argument("--goarch", default=None)
    parser.add_argument("--binary", default=None)
    parser.add_argument("--dist", default="dist")
    parser.add_argument("--version", default=None)
    args = parser.parse_args()

    goos = args.goos or go_env("GOOS")
    goarch = args.goarch or go_env("GOARCH")
    version = args.version or plugin_version(root)
    extension = EXTENSIONS.get(goos)
    if not extension:
        raise SystemExit(f"unsupported GOOS: {goos}")

    binary = pathlib.Path(args.binary) if args.binary else root / args.dist / f"{PLUGIN_ID}.{extension}"
    if not binary.is_absolute():
        binary = root / binary
    if not binary.exists():
        raise SystemExit(f"binary not found: {binary} (run the build step first)")

    dist = root / args.dist
    dist.mkdir(parents=True, exist_ok=True)
    archive = dist / f"{PLUGIN_ID}_{version}_{goos}_{goarch}.zip"
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
        bundle.write(binary, arcname=f"{PLUGIN_ID}.{extension}")

    checksum = sha256(archive)
    checksums = dist / "checksums.txt"
    lines = []
    if checksums.exists():
        lines = [line for line in checksums.read_text(encoding="utf-8").splitlines() if line.strip()]
    lines = [line for line in lines if not line.endswith(archive.name)]
    lines.append(f"{checksum}  {archive.name}")
    checksums.write_text("\n".join(lines) + "\n", encoding="utf-8")

    print(f"archive   : {archive}")
    print(f"sha256    : {checksum}")
    print(f"checksums : {checksums}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

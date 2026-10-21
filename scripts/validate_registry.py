#!/usr/bin/env python3
"""Validate this plugin against the CLIProxyAPI plugin-store contract.

The rules implemented here mirror the host source of truth
(`internal/pluginstore/{registry.go,github.go,checksum.go,install.go}`):

  * registry.json: schema_version 1/2, required fields, id/version patterns,
    repository must be https://github.com/<owner>/<repo>
  * release assets: `<id>_<version>_<goos>_<goarch>.zip` plus `checksums.txt`
  * archive layout: exactly one dynamic library, at the zip root, named
    `<id><ext>` (or `<id>-v<version><ext>`); no backslashes, absolute paths or
    `..` escapes; every entry a regular file
  * checksums.txt: `<sha256>  <name>` (whitespace separated, optional `*`)

Usage:
    python scripts/validate_registry.py                  # registry.json only
    python scripts/validate_registry.py dist/*.zip        # also check archives
"""

from __future__ import annotations

import hashlib
import json
import pathlib
import re
import sys
import zipfile

PLUGIN_ID = "anyrouter"
ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
VERSION_PATTERN = re.compile(r"^[0-9][0-9A-Za-z.+-]*$")
REPO_PATTERN = re.compile(r"^https://github\.com/[^/]+/[^/]+$")
ARCHIVE_PATTERN = re.compile(
    r"^(?P<id>[A-Za-z0-9][A-Za-z0-9._-]*)_(?P<version>[0-9][0-9A-Za-z.+-]*)_"
    r"(?P<goos>[a-z0-9]+)_(?P<goarch>[a-z0-9]+)\.zip$"
)
EXTENSIONS = {"windows": ".dll", "darwin": ".dylib"}
DEFAULT_EXTENSION = ".so"

problems: list[str] = []


def fail(message: str) -> None:
    problems.append(message)


def library_names(goos: str, version: str) -> set[str]:
    extension = EXTENSIONS.get(goos, DEFAULT_EXTENSION)
    return {f"{PLUGIN_ID}{extension}", f"{PLUGIN_ID}-v{version}{extension}"}


def check_registry(root: pathlib.Path) -> tuple[str, str]:
    path = root / "registry.json"
    if not path.exists():
        fail("registry.json is missing")
        return "", ""
    try:
        registry = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        fail(f"registry.json is not valid JSON: {error}")
        return "", ""

    schema = registry.get("schema_version")
    if schema not in (1, 2):
        fail(f"schema_version must be 1 or 2, found {schema!r}")

    plugins = registry.get("plugins")
    if not isinstance(plugins, list) or not plugins:
        fail("registry.json needs a non-empty plugins array")
        return "", ""

    version = ""
    seen: set[str] = set()
    for index, plugin in enumerate(plugins):
        where = f"plugins[{index}]"
        for field in ("id", "name", "description", "author"):
            if not str(plugin.get(field, "")).strip():
                fail(f"{where}: missing required field {field}")
        identifier = str(plugin.get("id", "")).strip()
        if identifier and not ID_PATTERN.match(identifier):
            fail(f"{where}: invalid plugin id {identifier!r}")
        if identifier in seen:
            fail(f"{where}: duplicate plugin id {identifier!r}")
        seen.add(identifier)
        if identifier != PLUGIN_ID:
            fail(f"{where}: this repository publishes {PLUGIN_ID!r}, found {identifier!r}")

        candidate = str(plugin.get("version", "")).strip()
        if candidate:
            if not VERSION_PATTERN.match(candidate):
                fail(f"{where}: invalid version {candidate!r} (no leading 'v')")
            version = candidate

        install_type = str((plugin.get("install") or {}).get("type", "")).strip().lower()
        if install_type in ("", "github-release"):
            repository = str(plugin.get("repository", "")).strip()
            if not REPO_PATTERN.match(repository):
                fail(f"{where}: repository must be https://github.com/<owner>/<repo>, found {repository!r}")
        elif install_type == "direct":
            if schema != 2:
                fail(f"{where}: direct install requires schema_version 2")
            if not candidate:
                fail(f"{where}: direct install requires a version")
        else:
            fail(f"{where}: unsupported install type {install_type!r}")
    return PLUGIN_ID, version


def check_archive(path: pathlib.Path, version: str, checksums: dict[str, str]) -> None:
    match = ARCHIVE_PATTERN.match(path.name)
    if not match:
        fail(f"{path.name}: archive name must be <id>_<version>_<goos>_<goarch>.zip")
        return
    goos = match.group("goos")
    archive_version = match.group("version")
    if match.group("id") != PLUGIN_ID:
        fail(f"{path.name}: archive id must be {PLUGIN_ID}")
    if version and archive_version != version:
        fail(f"{path.name}: archive version {archive_version!r} != registry version {version!r}")

    try:
        with zipfile.ZipFile(path) as bundle:
            entries = bundle.infolist()
            wanted = library_names(goos, archive_version)
            libraries = []
            for entry in entries:
                name = entry.filename
                if "\\" in name:
                    fail(f"{path.name}: entry {name!r} uses backslash separators")
                if name.startswith("/") or name.startswith("\\"):
                    fail(f"{path.name}: entry {name!r} is absolute")
                if name.startswith("../") or "/../" in name:
                    fail(f"{path.name}: entry {name!r} escapes the archive root")
                if entry.is_dir():
                    continue
                if name.lower().endswith((".so", ".dll", ".dylib")):
                    libraries.append(name)
            if not libraries:
                fail(f"{path.name}: no dynamic library inside")
            if len(libraries) > 1:
                fail(f"{path.name}: multiple dynamic libraries inside: {libraries}")
            for name in libraries:
                if name not in wanted:
                    if "/" in name:
                        fail(f"{path.name}: {name!r} must be at the archive root, not in a subdirectory")
                    else:
                        fail(f"{path.name}: library must be named {' or '.join(sorted(wanted))}, found {name!r}")
    except zipfile.BadZipFile as error:
        fail(f"{path.name}: not a readable zip: {error}")
        return

    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    recorded = checksums.get(path.name)
    if recorded is None:
        fail(f"checksums.txt has no entry for {path.name}")
    elif recorded.lower() != digest:
        fail(f"checksums.txt entry for {path.name} does not match the file")


def check_checksums(root: pathlib.Path) -> dict[str, str]:
    path = root / "dist" / "checksums.txt"
    if not path.exists():
        return {}
    out: dict[str, str] = {}
    for line_number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        fields = line.split()
        if len(fields) < 2:
            fail(f"checksums.txt line {line_number}: expected '<sha256>  <name>'")
            continue
        digest = fields[0].lower()
        if len(digest) != 64 or any(character not in "0123456789abcdef" for character in digest):
            fail(f"checksums.txt line {line_number}: invalid sha256")
            continue
        name = fields[1].lstrip("*")
        out[name] = digest
    return out


def main() -> int:
    root = pathlib.Path(__file__).resolve().parent.parent
    _, version = check_registry(root)
    checksums = check_checksums(root)

    archives = [pathlib.Path(argument) for argument in sys.argv[1:]]
    if not archives:
        candidate_dir = root / "dist"
        archives = sorted(candidate_dir.glob("*.zip")) if candidate_dir.exists() else []
    for archive in archives:
        check_archive(archive, version, checksums)

    if problems:
        print("plugin store validation FAILED:")
        for problem in problems:
            print(f"  - {problem}")
        return 1
    print(f"plugin store validation passed (registry + {len(archives)} archive(s))")
    return 0


if __name__ == "__main__":
    sys.exit(main())

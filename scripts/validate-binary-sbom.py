#!/usr/bin/env python3
"""Require an SPDX SBOM to enumerate every module embedded in a Go binary."""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from pathlib import Path


def embedded_modules(binary: Path) -> set[tuple[str, str]]:
    result = subprocess.run(
        ["go", "version", "-m", str(binary)],
        text=True,
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        raise ValueError(result.stderr.strip() or "go version -m failed")
    modules: set[tuple[str, str]] = set()
    for raw in result.stdout.splitlines():
        fields = raw.strip().split("\t")
        if len(fields) >= 3 and fields[0] == "dep":
            modules.add((fields[1], fields[2]))
    if not modules:
        raise ValueError("binary contains no enumerated Go dependencies")
    return modules


def spdx_packages(path: Path) -> set[tuple[str, str]]:
    value = json.loads(path.read_text(encoding="utf-8"))
    packages = value.get("packages") if isinstance(value, dict) else None
    if not isinstance(packages, list):
        raise ValueError("SPDX document has no package inventory")
    result: set[tuple[str, str]] = set()
    for package in packages:
        if not isinstance(package, dict):
            continue
        name = package.get("name")
        version = package.get("versionInfo")
        if isinstance(name, str) and isinstance(version, str):
            result.add((name, version))
    return result


def spdx_file_sha256(path: Path) -> set[str]:
    value = json.loads(path.read_text(encoding="utf-8"))
    files = value.get("files") if isinstance(value, dict) else None
    if not isinstance(files, list) or not files:
        raise ValueError("SPDX document does not inventory the shipped binary file")
    checksums: set[str] = set()
    for entry in files:
        if not isinstance(entry, dict):
            continue
        for checksum in entry.get("checksums", []):
            if (
                isinstance(checksum, dict)
                and checksum.get("algorithm") == "SHA256"
                and isinstance(checksum.get("checksumValue"), str)
            ):
                checksums.add(checksum["checksumValue"].lower())
    return checksums


def validate(binary: Path, sbom: Path) -> int:
    expected = embedded_modules(binary)
    actual = spdx_packages(sbom)
    binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    if binary_digest not in spdx_file_sha256(sbom):
        print("binary SBOM does not contain the shipped binary SHA-256", file=sys.stderr)
        return 1
    missing = sorted(expected - actual)
    if missing:
        preview = ", ".join(f"{name}@{version}" for name, version in missing[:8])
        print(
            f"binary SBOM is missing {len(missing)} of {len(expected)} embedded dependencies: {preview}",
            file=sys.stderr,
        )
        return 1
    print(f"binary SBOM verified: {len(expected)} embedded dependencies inventoried")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print("usage: validate-binary-sbom.py <go-binary> <spdx-json>", file=sys.stderr)
        return 2
    try:
        return validate(Path(argv[1]), Path(argv[2]))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"binary SBOM validation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))

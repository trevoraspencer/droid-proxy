#!/usr/bin/env python3
"""Validate the exact permission maps in the two trusted workflows.

This intentionally parses only the mapping shape used by these files. Comments are
removed before parsing, duplicate keys fail, and scalar permission shortcuts fail.
"""

from __future__ import annotations

import sys
from pathlib import Path


def lines(path: Path) -> list[tuple[int, str, int]]:
    result: list[tuple[int, str, int]] = []
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        content = raw.split("#", 1)[0].rstrip()
        if not content.strip():
            continue
        result.append((number, content.strip(), len(content) - len(content.lstrip())))
    return result


def mapping_after(records: list[tuple[int, str, int]], index: int) -> dict[str, str]:
    number, text, indent = records[index]
    if text == "permissions: {}":
        return {}
    if text != "permissions:":
        raise ValueError(f"line {number}: permissions must be an explicit mapping")
    values: dict[str, str] = {}
    for child_number, child, child_indent in records[index + 1 :]:
        if child_indent <= indent:
            break
        if child_indent != indent + 2 or ":" not in child:
            raise ValueError(f"line {child_number}: invalid permissions mapping entry")
        key, value = (part.strip() for part in child.split(":", 1))
        if key in values:
            raise ValueError(f"line {child_number}: duplicate permission {key}")
        if value not in {"read", "write", "none"}:
            raise ValueError(f"line {child_number}: invalid permission value {value!r}")
        values[key] = value
    return values


def permission_maps(path: Path) -> dict[str, dict[str, str]]:
    records = lines(path)
    result: dict[str, dict[str, str]] = {}
    in_jobs = False
    current_job: str | None = None
    for index, (_, text, indent) in enumerate(records):
        if indent == 0 and text == "jobs:":
            in_jobs = True
            continue
        if indent == 0 and text.startswith("permissions"):
            if "workflow" in result:
                raise ValueError("duplicate workflow permission map")
            result["workflow"] = mapping_after(records, index)
            continue
        if in_jobs and indent == 2 and text.endswith(":"):
            current_job = text[:-1]
            continue
        if in_jobs and current_job and indent == 4 and text.startswith("permissions"):
            key = f"job:{current_job}"
            if key in result:
                raise ValueError(f"duplicate permissions for {current_job}")
            result[key] = mapping_after(records, index)
    return result


def main() -> int:
    expected = {
        ".github/workflows/ci.yml": {"workflow": {"contents": "read"}},
        ".github/workflows/release.yml": {
            "workflow": {},
            "job:build": {"contents": "read"},
            "job:publish": {"attestations": "write", "contents": "write", "id-token": "write"},
        },
    }
    failures: list[str] = []
    for name, wanted in expected.items():
        try:
            actual = permission_maps(Path(name))
        except (OSError, ValueError) as exc:
            failures.append(f"{name}: {exc}")
            continue
        if actual != wanted:
            failures.append(f"{name}: permissions {actual!r}, expected {wanted!r}")
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("workflow permission maps are exact")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

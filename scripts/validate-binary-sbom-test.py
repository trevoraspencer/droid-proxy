#!/usr/bin/env python3
"""Unit tests for the binary-to-SPDX inventory validator."""

from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("validate-binary-sbom.py")
SPEC = importlib.util.spec_from_file_location("validate_binary_sbom", SCRIPT)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"unable to load {SCRIPT}")
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


class ValidatorTests(unittest.TestCase):
    def test_requires_binary_digest_and_every_embedded_dependency(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "droid-proxy"
            binary.write_bytes(b"reviewed binary")
            digest = hashlib.sha256(binary.read_bytes()).hexdigest()
            sbom = root / "binary.spdx.json"
            document = {
                "files": [{"checksums": [{"algorithm": "SHA256", "checksumValue": digest}]}],
                "packages": [
                    {"name": "example.com/one", "versionInfo": "v1.0.0"},
                    {"name": "example.com/two", "versionInfo": "v2.0.0"},
                ],
            }
            sbom.write_text(json.dumps(document), encoding="utf-8")
            expected = {("example.com/one", "v1.0.0"), ("example.com/two", "v2.0.0")}
            with mock.patch.object(validator, "embedded_modules", return_value=expected):
                self.assertEqual(validator.validate(binary, sbom), 0)
                document["packages"].pop()
                sbom.write_text(json.dumps(document), encoding="utf-8")
                with redirect_stderr(io.StringIO()):
                    self.assertEqual(validator.validate(binary, sbom), 1)
                document["packages"].append({"name": "example.com/two", "versionInfo": "v2.0.0"})
                document["files"][0]["checksums"][0]["checksumValue"] = "0" * 64
                sbom.write_text(json.dumps(document), encoding="utf-8")
                with redirect_stderr(io.StringIO()):
                    self.assertEqual(validator.validate(binary, sbom), 1)


if __name__ == "__main__":
    unittest.main()

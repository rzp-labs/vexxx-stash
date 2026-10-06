#!/usr/bin/env python3
"""Fork version identity, independent of inherited upstream tags."""
from pathlib import Path
import re
import subprocess

NORMAL = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


def validate_version(version):
    if not isinstance(version, str) or not NORMAL.fullmatch(version) or len(version) > 120:
        raise ValueError("VERSION must be MAJOR.MINOR.PATCH without leading zeros or suffixes")
    return version


def read_version(path=Path(__file__).resolve().parent.parent / "VERSION"):
    return validate_version(Path(path).read_text().removesuffix("\n"))


def development_version(version, revision):
    validate_version(version)
    if not isinstance(revision, str) or not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("development version requires the full commit SHA")
    return version + "-dev+sha." + revision[:12]


if __name__ == "__main__":
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True, timeout=30).strip()
    print(development_version(read_version(), revision))

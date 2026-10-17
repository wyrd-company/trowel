#!/usr/bin/env python3

"""Verify that a retained release handoff belongs to an exact Trowel run."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


VERSION = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)")
COMMIT = re.compile(r"[0-9a-f]{40}")


class HandoffError(ValueError):
    """The retained artifact does not belong to the requested release run."""


def verify(path: Path, commit: str, expected_version: str | None) -> str:
    if COMMIT.fullmatch(commit) is None:
        raise HandoffError("expected commit must be a full lowercase Git object ID")
    try:
        manifest = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise HandoffError(f"cannot read handoff: {error}") from error
    if not isinstance(manifest, dict):
        raise HandoffError("handoff must be a JSON object")

    version = manifest.get("version")
    if not isinstance(version, str) or VERSION.fullmatch(version) is None:
        raise HandoffError("handoff version must be a bare stable SemVer")
    if manifest.get("tag") != version:
        raise HandoffError("handoff tag must equal its version")
    if expected_version is not None and version != expected_version:
        raise HandoffError(
            f"handoff version {version!r} does not match requested tag {expected_version!r}"
        )
    if manifest.get("product") != "trowel":
        raise HandoffError("handoff product must be trowel")
    source = manifest.get("source")
    if not isinstance(source, dict) or source.get("repository") != "wyrd-company/trowel":
        raise HandoffError("handoff source repository must be wyrd-company/trowel")
    if source.get("commit") != commit:
        raise HandoffError("handoff source commit does not match the release workflow run")
    return version


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--expected-version")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        version = verify(args.manifest, args.commit, args.expected_version)
    except HandoffError as error:
        print(f"verify-release-handoff: {error}", file=sys.stderr)
        return 2
    print(version)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

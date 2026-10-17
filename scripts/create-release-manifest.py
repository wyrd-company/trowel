#!/usr/bin/env python3

"""Create the immutable repo.wyrd.foo handoff for one Trowel release."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path


VERSION_PATTERN = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)")
COMMIT_PATTERN = re.compile(r"[0-9a-f]{40}")
FORMATS = (
    ("archive", "tar.gz", "amd64", "x86_64"),
    ("archive", "tar.gz", "arm64", "aarch64"),
    ("package", "deb", "amd64", "x86_64"),
    ("package", "deb", "arm64", "aarch64"),
    ("package", "rpm", "amd64", "x86_64"),
    ("package", "rpm", "arm64", "aarch64"),
)


# Both must equal the trowel entry in repo.wyrd.foo config/products.json.
PACKAGE = {
    "name": "trowel",
    "binary": "trowel",
    "description": "Relay HTTP webhooks into NATS JetStream and JetStream messages out to HTTP or commands",
    "homepage": "https://github.com/wyrd-company/trowel",
    "license": "Apache-2.0",
    "maintainer": "Wyrd Company <support@wyrd.company>",
}
PUBLISH = {
    "apt": {"suite": "stable", "component": "main"},
    "rpm": {"channel": "stable"},
    "aur": {
        "package": "trowel-bin",
        "installation": {
            "files": {
                "packaging/trowel.service": "/usr/lib/systemd/system/trowel.service",
            },
            "hooks": {
                "pre-install": "packaging/preinstall.sh",
                "post-install": "packaging/postinstall.sh",
                "pre-remove": "packaging/preremove.sh",
                "post-remove": "packaging/postremove.sh",
            },
        },
    },
}


class ManifestError(ValueError):
    """The release inputs cannot form the required manifest."""


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as artifact:
        for chunk in iter(lambda: artifact.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def expected_artifacts(version: str) -> list[tuple[str, str, str, str]]:
    return [
        (kind, artifact_format, arch, f"trowel_{version}_linux_{suffix}.{artifact_format}")
        for kind, artifact_format, arch, suffix in FORMATS
    ]


def create_manifest(artifacts_dir: Path, version: str, commit: str) -> dict[str, object]:
    if VERSION_PATTERN.fullmatch(version) is None:
        raise ManifestError(f"version must be a bare stable SemVer: {version!r}")
    if COMMIT_PATTERN.fullmatch(commit) is None:
        raise ManifestError("commit must be a full lowercase Git object ID")
    if not artifacts_dir.is_dir():
        raise ManifestError(f"artifact directory does not exist: {artifacts_dir}")

    expected = expected_artifacts(version)
    expected_names = {filename for _, _, _, filename in expected}
    release_assets = {
        path.name
        for path in artifacts_dir.iterdir()
        if path.is_file() and path.name.endswith((".tar.gz", ".deb", ".rpm"))
    }
    missing = sorted(expected_names - release_assets)
    extra = sorted(release_assets - expected_names)
    if missing or extra:
        details = []
        if missing:
            details.append("missing: " + ", ".join(missing))
        if extra:
            details.append("unexpected: " + ", ".join(extra))
        raise ManifestError("release must contain exactly six package assets (" + "; ".join(details) + ")")

    base_url = f"https://github.com/wyrd-company/trowel/releases/download/{version}"
    artifacts = []
    for kind, artifact_format, arch, filename in expected:
        path = artifacts_dir / filename
        artifacts.append(
            {
                "kind": kind,
                "format": artifact_format,
                "os": "linux",
                "arch": arch,
                "filename": filename,
                "url": f"{base_url}/{filename}",
                "sha256": sha256(path),
            }
        )

    return {
        "schema_version": 1,
        "product": "trowel",
        "version": version,
        "tag": version,
        "source": {
            "repository": "wyrd-company/trowel",
            "commit": commit,
        },
        "package": PACKAGE,
        "publish": PUBLISH,
        "artifacts": artifacts,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts-dir", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        manifest = create_manifest(args.artifacts_dir, args.version, args.commit)
    except ManifestError as error:
        print(f"create-release-manifest: {error}", file=sys.stderr)
        return 2
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

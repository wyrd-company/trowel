#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 5 ]]; then
  echo "usage: validate-publisher-contract.sh PUBLISHER COMMIT MANIFEST ARTIFACTS OUTPUT" >&2
  exit 2
fi

publisher="$1"
expected_commit="$2"
manifest="$3"
artifacts="$4"
output="$5"

if [[ ! "$expected_commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "publisher contract must be pinned to a full commit SHA" >&2
  exit 2
fi
if [[ "$(git -C "$publisher" rev-parse HEAD)" != "$expected_commit" ]]; then
  echo "publisher checkout does not match the required commit" >&2
  exit 2
fi

repository_script="$publisher/scripts/repository.py"
public_key="$publisher/pubkey.gpg"
allowlist="$publisher/config/products.json"
if [[ ! -f "$repository_script" || ! -f "$public_key" || ! -f "$allowlist" ]]; then
  echo "publisher checkout does not expose the package contract" >&2
  exit 2
fi

inbox="$output/inbox"
stage="$output/stage"
aur="$output/aur"
mkdir -p "$inbox" "$stage" "$aur"

python3 "$repository_script" validate "$manifest"
git init --quiet --initial-branch=release-manifests "$inbox"
git -C "$inbox" config user.name "Package Contract Test"
git -C "$inbox" config user.email "package-contract@example.invalid"
git -C "$inbox" commit --quiet --allow-empty -m "Initialize package inbox"
submission="$(python3 "$repository_script" submit "$manifest" "$inbox")"
IFS=$'\t' read -r state submitted_path <<<"$submission"
if [[ "$state" != "created" ]]; then
  echo "publisher did not create a new immutable inbox manifest" >&2
  exit 2
fi
if [[ ! "$submitted_path" =~ ^releases/trowel/[0-9]+\.[0-9]+\.[0-9]+\.json$ ]]; then
  echo "publisher returned an unexpected immutable inbox path: $submitted_path" >&2
  exit 2
fi
git -C "$inbox" add -- "$submitted_path"
git -C "$inbox" commit --quiet -m "Add package release manifest"
inbox_commit="$(git -C "$inbox" rev-parse HEAD)"
submitted_manifest="$inbox/$submitted_path"
python3 "$repository_script" validate-queue \
  "$inbox" "$inbox_commit" "$submitted_path" "$allowlist"
python3 "$repository_script" stage "$submitted_manifest" "$artifacts" "$stage" "$public_key"
python3 "$repository_script" render-aur "$submitted_manifest" "$aur" --artifacts "$artifacts"
test -s "$stage/.new-rpms"
test -s "$aur/PKGBUILD"
test -s "$aur/trowel-bin.install"

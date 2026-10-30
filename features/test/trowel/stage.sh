#!/usr/bin/env bash

# Copies the Feature and its tests to a directory for `devcontainer features
# test`, dropping installsAfter entries whose Feature is not published. The CLI
# resolves every installsAfter entry and refuses to build when one is missing,
# which holds for the Ansible Feature until its first release. The copy is the
# only place entries are dropped; the Feature source keeps the full list, and
# once every entry resolves the copy matches the source.
set -euo pipefail

source_root="$(cd "$(dirname "$0")/../.." && pwd)"
destination="${1:?usage: stage.sh <destination>}"

rm -rf "${destination}"
mkdir -p "${destination}"
cp -R "${source_root}/src" "${source_root}/test" "${destination}/"

metadata="${destination}/src/trowel/devcontainer-feature.json"
kept="$(mktemp)"
trap 'rm -f "${kept}"' EXIT
jq -c '.installsAfter[]?' "${metadata}" | while read -r entry; do
    reference="$(jq -r . <<<"${entry}")"
    if devcontainer features info manifest "${reference}" >/dev/null 2>&1; then
        echo "${entry}" >>"${kept}"
    else
        echo "[stage] ${reference} is not published; leaving it out of the test copy." >&2
    fi
done
jq --slurpfile kept <(cat "${kept}" 2>/dev/null || true) '.installsAfter = $kept' "${metadata}" >"${metadata}.new"
mv "${metadata}.new" "${metadata}"

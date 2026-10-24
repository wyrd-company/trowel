#!/usr/bin/env bash

# Records docs/user/assets/demo.gif with vhs inside a container that also has
# trowel, nats-server, and the nats CLI. Needs Docker; gifsicle is optional.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
image=trowel-demo:local

docker build --file "${repo_root}/docs/demo/Dockerfile" --tag "${image}" "${repo_root}"
docker run --rm --volume "${repo_root}:/vhs" --workdir /vhs "${image}" docs/demo/demo.tape

if command -v gifsicle >/dev/null 2>&1; then
    gifsicle -O3 --lossy=40 --batch "${repo_root}/docs/user/assets/demo.gif"
fi
echo "Wrote docs/user/assets/demo.gif"

#!/usr/bin/env bash

# Starts the Feature under s6-overlay next to a NATS server and checks that
# the service runs as the service user, relays a webhook, and restarts.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
base_image="${BASE_IMAGE:-ghcr.io/wyrd-company/devcontainers/base:noble}"
image="${1:-trowel-feature-runtime:test}"
suffix="${RANDOM}-$$"
network="trowel-runtime-${suffix}"
nats="trowel-nats-${suffix}"
name="trowel-runtime-${suffix}"
command='/usr/local/bin/trowel -config /etc/trowel/config.yml'

cleanup() {
    docker rm --force "${name}" "${nats}" >/dev/null 2>&1 || true
    docker network rm "${network}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

ready() {
    docker exec "${name}" curl --fail --silent http://127.0.0.1:8080/readyz >/dev/null 2>&1
}

docker network create "${network}" >/dev/null
docker run --detach --name "${nats}" --network "${network}" nats:2.11-alpine --jetstream >/dev/null

docker build \
    --file "${repo_root}/features/test/trowel/runtime.Dockerfile" \
    --build-arg "BASE_IMAGE=${base_image}" \
    --build-arg "NATS_URL=nats://${nats}:4222" \
    --tag "${image}" \
    "${repo_root}/features/src/trowel"

docker run --detach \
    --name "${name}" \
    --network "${network}" \
    --env SAMPLE_RUNTIME_VALUE=sample-runtime-value \
    "${image}" >/dev/null

for _ in $(seq 1 60); do
    ready && break
    sleep 0.5
done
ready

trowel_pid="$(docker exec "${name}" pgrep --full --exact "${command}" | head -n 1)"
test -n "${trowel_pid}"
test "$(docker exec "${name}" ps -o user= -p "${trowel_pid}" | tr -d ' ')" = vscode
docker exec --user vscode "${name}" sh -c \
    "tr '\0' '\n' </proc/${trowel_pid}/environ | grep -qx SAMPLE_RUNTIME_VALUE=sample-runtime-value"

docker run --rm --network "${network}" natsio/nats-box:latest \
    nats --server "nats://${nats}:4222" stream add SAMPLE --subjects 'trowel.>' --defaults >/dev/null
status="$(docker exec "${name}" curl --silent --output /dev/null --write-out '%{http_code}' \
    --header 'Content-Type: application/json' --data '{"sample":true}' \
    http://127.0.0.1:8080/hooks/example)"
test "${status}" = 202

previous_pid="${trowel_pid}"
docker exec "${name}" /command/s6-svc -r /run/service/trowel
for _ in $(seq 1 60); do
    trowel_pid="$(docker exec "${name}" pgrep --full --exact "${command}" | head -n 1 || true)"
    if [ -n "${trowel_pid}" ] && [ "${trowel_pid}" != "${previous_pid}" ] && ready; then
        break
    fi
    sleep 0.5
done
test -n "${trowel_pid}"
test "${trowel_pid}" != "${previous_pid}"
ready

printf 'Trowel s6 runtime checks passed.\n'

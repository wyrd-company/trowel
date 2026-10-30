#!/usr/bin/env bash

# Starts the Feature under s6-overlay next to a NATS server and checks that
# the service runs as the service user, relays a webhook, and restarts. Then
# repeats the start with a stand-in for the Ansible Feature's ansible-setup
# oneshot: Trowel starts only after the oneshot succeeds, and stays down when
# it fails.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
test_dir="${repo_root}/features/test/trowel"
base_image="${BASE_IMAGE:-ghcr.io/wyrd-company/devcontainers/base:noble}"
image="${1:-trowel-feature-runtime:test}"
suffix="${RANDOM}-$$"
network="trowel-runtime-${suffix}"
nats="trowel-nats-${suffix}"
name="trowel-runtime-${suffix}"
command='/usr/local/bin/trowel -config /etc/trowel/config.yml'
images=()

cleanup() {
    docker rm --force "${name}" "${nats}" >/dev/null 2>&1 || true
    docker network rm "${network}" >/dev/null 2>&1 || true
    if [ "${#images[@]}" -gt 0 ]; then
        docker image rm --force "${images[@]}" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

ready() {
    docker exec "${name}" curl --fail --silent http://127.0.0.1:8080/readyz >/dev/null 2>&1
}

wait_ready() {
    local _
    for _ in $(seq 1 60); do
        ready && return 0
        sleep 0.5
    done
    ready
}

# build_image <tag> <base image>: installs the Feature onto the base image.
build_image() {
    images+=("$1")
    docker build \
        --file "${test_dir}/runtime.Dockerfile" \
        --build-arg "BASE_IMAGE=$2" \
        --build-arg "NATS_URL=nats://${nats}:4222" \
        --tag "$1" \
        "${repo_root}/features/src/trowel"
}

# build_fixture <tag> <success|fail>: a base image carrying an ansible-setup
# oneshot, as the Ansible Feature installs it before Trowel's Feature.
build_fixture() {
    images+=("$1")
    docker build \
        --file "${test_dir}/trowel-ansible-setup/Dockerfile" \
        --build-arg "BASE_IMAGE=${base_image}" \
        --build-arg "ANSIBLE_SETUP_MODE=$2" \
        --tag "$1" \
        "${test_dir}/trowel-ansible-setup"
}

start_container() {
    docker rm --force "${name}" >/dev/null 2>&1 || true
    docker run --detach \
        --name "${name}" \
        --network "${network}" \
        --env SAMPLE_RUNTIME_VALUE=sample-runtime-value \
        --env VAULT_ADDR=unix:///run/sample/vault.sock \
        --env SSH_AUTH_SOCK=/run/sample/agent.sock \
        "$1" >/dev/null
}

docker network create "${network}" >/dev/null
docker run --detach --name "${nats}" --network "${network}" nats:2.11-alpine --jetstream >/dev/null

# Standalone: no Ansible Feature, so nothing delays or blocks Trowel.
build_image "${image}" "${base_image}"
start_container "${image}"
wait_ready

docker exec "${name}" test ! -e /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/ansible-setup
docker exec "${name}" test ! -e /etc/s6-overlay/s6-rc.d/ansible-setup

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

# With Ansible: Trowel starts only after ansible-setup succeeds, and keeps the
# container environment, such as the Vault and SSH agent socket variables.
build_fixture "${image}-ansible-base" success
build_image "${image}-ansible" "${image}-ansible-base"
start_container "${image}-ansible"
# Trowel is seen only after ansible-setup wrote its marker: the marker is read
# after the process is seen, so a missing marker proves Trowel started first.
trowel_pid=
for _ in $(seq 1 300); do
    trowel_pid="$(docker exec "${name}" pgrep --full --exact "${command}" 2>/dev/null | head -n 1 || true)"
    [ -n "${trowel_pid}" ] && break
    sleep 0.1
done
test -n "${trowel_pid}"
if ! docker exec "${name}" test -f /run/ansible-fixture/done; then
    echo "Trowel started before ansible-setup finished." >&2
    exit 1
fi
wait_ready
docker exec "${name}" test -f /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/ansible-setup
docker exec --user vscode "${name}" sh -c \
    "tr '\0' '\n' </proc/${trowel_pid}/environ | grep -qx VAULT_ADDR=unix:///run/sample/vault.sock"
docker exec --user vscode "${name}" sh -c \
    "tr '\0' '\n' </proc/${trowel_pid}/environ | grep -qx SSH_AUTH_SOCK=/run/sample/agent.sock"

# With a failing ansible-setup, Trowel never starts, and the container stays up.
build_fixture "${image}-ansible-fail-base" fail
build_image "${image}-ansible-fail" "${image}-ansible-fail-base"
start_container "${image}-ansible-fail"
sleep 10
test "$(docker inspect --format '{{.State.Running}}' "${name}")" = true
if docker exec "${name}" pgrep --full --exact "${command}" >/dev/null; then
    echo "Trowel started although ansible-setup failed." >&2
    exit 1
fi
if ready; then
    echo "Trowel became ready although ansible-setup failed." >&2
    exit 1
fi

printf 'Trowel s6 runtime checks passed.\n'

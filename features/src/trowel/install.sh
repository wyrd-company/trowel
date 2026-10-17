#!/usr/bin/env bash

set -euo pipefail

log() { echo "[trowel] $*"; }
err() { echo "[trowel] ERROR: $*" >&2; exit 1; }

feature_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
release_base=https://github.com/wyrd-company/trowel/releases

pick_service_user() {
    local requested="${1:-automatic}"
    local candidate
    if [ -n "${requested}" ] && [ "${requested}" != automatic ] && [ "${requested}" != auto ]; then
        id -u "${requested}" >/dev/null 2>&1 \
            || err "Requested service user '${requested}' does not exist."
        echo "${requested}"
        return
    fi
    for candidate in "${_REMOTE_USER:-}" "${_CONTAINER_USER:-}" vscode root; do
        if [ -n "${candidate}" ] && id -u "${candidate}" >/dev/null 2>&1; then
            echo "${candidate}"
            return
        fi
    done
    err "Unable to resolve a service user."
}

# Option values land in a YAML file and in ${NAME} expansion, so anything
# beyond plain address characters is rejected.
check_value() {
    local option="$1" value="$2" pattern="$3"
    [[ "${value}" =~ ${pattern} ]] || err "${option} '${value}' is not allowed."
}

[ "$(id -u)" -eq 0 ] || err "This Feature must run as root."
[ -r /etc/os-release ] || err "Unable to detect Linux distribution."

requested_version="${VERSION:-latest}"
listen="${LISTEN:-127.0.0.1:8080}"
nats_url="${NATSURL:-nats://127.0.0.1:4222}"
service_user_request="${SERVICEUSER:-automatic}"

# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-} ${ID_LIKE:-}" in
    *debian*|*ubuntu*) ;;
    *) err "This Feature currently supports Debian/Ubuntu-based images." ;;
esac

for required_path in /init /command/s6-rc /etc/s6-overlay/s6-rc.d /etc/s6-overlay/user-bundles.d/user/contents.d; do
    [ -e "${required_path}" ] \
        || err "This Feature requires an s6-overlay 3 image with /init as PID 1."
done

case "$(uname -m)" in
    x86_64|amd64) architecture=x86_64 ;;
    arm64|aarch64) architecture=aarch64 ;;
    *) err "Unsupported architecture: $(uname -m)." ;;
esac

check_value listen "${listen}" '^[][A-Za-z0-9.:_-]*:[0-9]+$'
check_value natsUrl "${nats_url}" '^(nats|tls|ws|wss)://[][A-Za-z0-9.:_@/,-]+$'

service_user="$(pick_service_user "${service_user_request}")"
service_home="$(getent passwd "${service_user}" | cut -d: -f6)"
[ -n "${service_home}" ] || err "Unable to resolve the home directory for ${service_user}."

# Rendered secret files from the OpenBao Agent Feature are group-readable.
if [ "${service_user}" != root ] && getent group openbao-secrets >/dev/null 2>&1; then
    usermod -aG openbao-secrets "${service_user}"
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends ca-certificates curl

download_dir="$(mktemp -d)"
trap 'rm -rf "${download_dir}"' EXIT

# CI places a binary built from the checkout here to test unreleased changes.
# Published Features never contain it.
if [ -x "${feature_dir}/ci-binary/trowel" ]; then
    log "Installing the CI-built binary from the Feature directory."
    install -m 0755 "${feature_dir}/ci-binary/trowel" /usr/local/bin/trowel
    normalized_version=ci
else
    if [ "${requested_version}" = latest ]; then
        log "Resolving the latest stable release..."
        latest_url="$(curl --fail --location --silent --show-error --retry 5 --retry-all-errors \
            --output /dev/null --write-out '%{url_effective}' "${release_base}/latest")"
        normalized_version="${latest_url##*/}"
    else
        normalized_version="${requested_version}"
    fi
    [[ "${normalized_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || err "version must be 'latest' or a release such as '0.1.0', not '${normalized_version}'."

    archive_name="trowel_${normalized_version}_linux_${architecture}.tar.gz"
    release_url="${release_base}/download/${normalized_version}"
    log "Downloading ${archive_name}..."
    for asset in "${archive_name}" checksums.txt; do
        curl --fail --location --silent --show-error --retry 5 --retry-all-errors \
            --output "${download_dir}/${asset}" "${release_url}/${asset}"
    done
    expected_checksum="$(awk -v name="${archive_name}" '$2 == name { print $1 }' "${download_dir}/checksums.txt")"
    [[ "${expected_checksum}" =~ ^[0-9a-f]{64}$ ]] || err "checksums.txt does not list ${archive_name}."
    printf '%s  %s\n' "${expected_checksum}" "${download_dir}/${archive_name}" | sha256sum --check --status \
        || err "Checksum verification failed for ${archive_name}."
    tar -xzf "${download_dir}/${archive_name}" -C "${download_dir}" trowel
    install -m 0755 "${download_dir}/trowel" /usr/local/bin/trowel
fi

config_dir=/etc/trowel
config_file="${config_dir}/config.yml"
install -d -m 0755 -o root -g root "${config_dir}"
cat >"${config_file}" <<EOF
# Starter configuration from the Trowel devcontainer Feature. Bind-mount a
# project configuration over this file; see https://wyrd.foo/docs/trowel.
server:
  listen: "${listen}"

nats:
  url: "${nats_url}"

# POST /hooks/example publishes to trowel.example. A JetStream stream must
# cover that subject before publishes succeed.
webhooks:
  - path: /hooks/example
    subject: trowel.example
EOF
chmod 0644 "${config_file}"

printf -v quoted_config '%q' "${config_file}"
cat >/usr/local/bin/trowel-service <<EOF
#!/usr/bin/env bash
set -euo pipefail

secret_file=/run/openbao/secrets/trowel.env
if [ -x /usr/local/bin/openbao-wait-for-secrets ]; then
    /usr/local/bin/openbao-wait-for-secrets trowel
fi
if [ -e "\${secret_file}" ] && [ ! -r "\${secret_file}" ]; then
    echo "[trowel] ERROR: \${secret_file} exists but is not readable by \$(id -un)." >&2
    exit 1
fi
if [ -r "\${secret_file}" ]; then
    # Values are literal text, not shell. The container environment takes precedence.
    while IFS= read -r line || [ -n "\${line}" ]; do
        line="\${line#"\${line%%[![:space:]]*}"}"
        case "\${line}" in
            ''|'#'*) continue ;;
        esac
        name="\${line%%=*}"
        if [ "\${name}" = "\${line}" ] || ! [[ "\${name}" =~ ^[A-Za-z_][A-Za-z0-9_]*\$ ]]; then
            echo "[trowel] ERROR: \${secret_file} contains a line that is not NAME=value." >&2
            exit 1
        fi
        if [ -z "\${!name+x}" ]; then
            export "\${name}=\${line#*=}"
        fi
    done <"\${secret_file}"
fi

exec /usr/local/bin/trowel -config ${quoted_config} "\$@"
EOF
chmod 0755 /usr/local/bin/trowel-service

service_dir=/etc/s6-overlay/s6-rc.d/trowel
install -d -m 0755 "${service_dir}/dependencies.d"
printf 'longrun\n' >"${service_dir}/type"
touch "${service_dir}/dependencies.d/base"
if [ -d /etc/s6-overlay/s6-rc.d/openbao-secrets ]; then
    touch "${service_dir}/dependencies.d/openbao-secrets"
fi
printf -v quoted_user '%q' "${service_user}"
printf -v quoted_home '%q' "${service_home}"
cat >"${service_dir}/run" <<EOF
#!/command/with-contenv bash
exec s6-setuidgid ${quoted_user} env HOME=${quoted_home} USER=${quoted_user} /usr/local/bin/trowel-service
EOF
chmod 0755 "${service_dir}/run"
touch /etc/s6-overlay/user-bundles.d/user/contents.d/trowel

env HOME="${service_home}" USER="${service_user}" \
    /usr/local/bin/trowel -config "${config_file}" -validate >/dev/null 2>&1 \
    || err "The starter configuration did not validate."
rm -rf /var/lib/apt/lists/*
log "Installed Trowel ${normalized_version} as an s6 service running as ${service_user}."

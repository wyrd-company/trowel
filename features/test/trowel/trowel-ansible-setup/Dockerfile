# Stands in for the Ansible Feature, which ships separately. It installs the
# one thing Trowel's Feature reacts to: an ansible-setup s6-rc oneshot.
# ANSIBLE_SETUP_MODE=fail makes the oneshot fail instead of converge.
ARG BASE_IMAGE=ghcr.io/wyrd-company/devcontainers/base:noble
FROM ${BASE_IMAGE}
ARG ANSIBLE_SETUP_MODE=success
RUN set -eu; \
    service_dir=/etc/s6-overlay/s6-rc.d/ansible-setup; \
    mkdir -p "${service_dir}/dependencies.d"; \
    printf 'oneshot\n' >"${service_dir}/type"; \
    touch "${service_dir}/dependencies.d/base"; \
    printf '#!/command/with-contenv bash\nexec /usr/local/bin/ansible-setup-fixture\n' >"${service_dir}/up"; \
    chmod 0755 "${service_dir}/up"; \
    printf '#!/usr/bin/env bash\nset -eu\nsleep 3\n[ "%s" = fail ] && exit 1\nmkdir -p /run/ansible-fixture\ndate +%%s > /run/ansible-fixture/done\n' \
        "${ANSIBLE_SETUP_MODE}" >/usr/local/bin/ansible-setup-fixture; \
    chmod 0755 /usr/local/bin/ansible-setup-fixture; \
    touch /etc/s6-overlay/user-bundles.d/user/contents.d/ansible-setup

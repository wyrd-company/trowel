ARG BASE_IMAGE=ghcr.io/wyrd-company/devcontainers/base:noble
FROM ${BASE_IMAGE}
ARG NATS_URL
COPY . /tmp/trowel-feature
RUN VERSION=latest \
    LISTEN=127.0.0.1:8080 \
    NATSURL="${NATS_URL}" \
    SERVICEUSER=automatic \
    _REMOTE_USER=vscode \
    /tmp/trowel-feature/install.sh \
    && rm -rf /tmp/trowel-feature

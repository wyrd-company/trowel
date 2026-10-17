# The vhs image plus trowel built from this checkout, nats-server, the nats
# CLI, and the tools the tape types. docs/demo/record.sh builds and runs it.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/wyrd-company/trowel/internal/app.Version=demo" \
    -o /out/trowel ./cmd/trowel

FROM nats:2.11-alpine AS nats
FROM natsio/nats-box:latest AS nats-box

FROM ghcr.io/charmbracelet/vhs:v0.10.0
# The image is bookworm but its sources track "stable", which moved on, so
# pin them to the image's own release before installing.
RUN sed -i -e 's/^Suites: stable stable-updates$/Suites: bookworm bookworm-updates/' \
        -e 's/^Suites: stable-security$/Suites: bookworm-security/' \
        /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends curl jq openssl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/trowel /usr/local/bin/trowel
COPY --from=nats /usr/local/bin/nats-server /usr/local/bin/nats-server
COPY --from=nats-box /usr/local/bin/nats /usr/local/bin/nats

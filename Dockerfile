FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/wyrd-company/trowel/internal/app.Version=${VERSION}" \
    -o /out/trowel ./cmd/trowel

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget
RUN addgroup -S relay && adduser -S -G relay relay
COPY --from=build /out/trowel /usr/local/bin/trowel
ENV TROWEL_CONFIG=/config/config.yml
EXPOSE 8080/tcp
USER relay
HEALTHCHECK --interval=10s --timeout=3s --retries=6 CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/trowel"]

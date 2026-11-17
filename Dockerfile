# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b

FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
RUN apk add --no-cache git just
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
ARG BUILD_VERSION=0.0.0
ARG BUILD_COMMIT=unknown
RUN just version="${BUILD_VERSION}" commit_sha="${BUILD_COMMIT}" build \
    && mv build/yt-dlp-mcp-linux-* /usr/local/bin/yt-dlp-mcp

FROM ghcr.io/acidghost/yt-dlp-oci:2026.10.26-0@sha256:3f27a361ecb7810239701b6aa18701179690148601e5fe83636c49d781f01dbb
COPY --from=builder /usr/local/bin/yt-dlp-mcp /usr/local/bin/yt-dlp-mcp
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/yt-dlp-mcp"]

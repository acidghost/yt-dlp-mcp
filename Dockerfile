# syntax=docker/dockerfile:1.29.0@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183

FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
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

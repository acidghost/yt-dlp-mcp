# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

FROM golang:1.26.4-alpine@sha256:f23e8b227fb4493eabe03bede4d5a32d04092da71962f1fb79b5f7d1e6c2a17f AS builder
RUN apk add --no-cache git just
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
ARG BUILD_VERSION=0.0.0
ARG BUILD_COMMIT=unknown
RUN just version="${BUILD_VERSION}" commit_sha="${BUILD_COMMIT}" build \
    && mv build/yt-dlp-mcp-linux-* /usr/local/bin/yt-dlp-mcp

FROM python:3.14-slim@sha256:c845af9399020c7e562969a13689e929074a10fd057acd1b1fad06a2fb068e97
COPY --from=denoland/deno@sha256:ddaad47cbbbbd856d73bd0d50074a0e308c51671d83442eebb15f1039dd4a822 /usr/bin/deno /usr/bin/deno
RUN python3 -m pip install -U "yt-dlp[default]" \
 && groupadd --gid 1000 ytdlp \
 && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin ytdlp
COPY --from=builder /usr/local/bin/yt-dlp-mcp /usr/local/bin/yt-dlp-mcp
USER ytdlp:ytdlp
WORKDIR /wa
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/yt-dlp-mcp"]

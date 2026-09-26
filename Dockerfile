# syntax=docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e

FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
RUN apk add --no-cache git just
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
ARG BUILD_VERSION=0.0.0
ARG BUILD_COMMIT=unknown
RUN just version="${BUILD_VERSION}" commit_sha="${BUILD_COMMIT}" build \
    && mv build/yt-dlp-mcp-linux-* /usr/local/bin/yt-dlp-mcp

FROM python:3.14.7-slim@sha256:51dafde81dbdb6ebde285137a295cf18a47ca95234fe388a343719cb97305b3d
COPY --from=denoland/deno:2.9.6@sha256:2014dc167ece617ef7e7ba40631ac2234c59e75ce693e7cc2dc2602b3c87859d /usr/bin/deno /usr/bin/deno
COPY requirements.txt /tmp/requirements.txt
RUN python3 -m pip install --no-cache-dir --require-hashes -r /tmp/requirements.txt \
 && python3 -m pip check \
 && groupadd --gid 1000 ytdlp \
 && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin ytdlp
COPY --from=builder /usr/local/bin/yt-dlp-mcp /usr/local/bin/yt-dlp-mcp
USER ytdlp:ytdlp
WORKDIR /wa
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/yt-dlp-mcp"]

# yt-dlp-mcp

A tiny HTTP MCP server for YouTube search and subtitle extraction using the `yt-dlp` CLI.

## Tools

### `search_videos`

Search YouTube via `ytsearchN:query`.

```json
{
  "query": "lo-fi music",
  "limit": 5,
  "timeout_ms": 60000
}
```

Returns structured results with title, URL, id, duration, uploader, and upload date when available.

### `download_subtitles`

Download auto-generated subtitles and return normalized plain text by default.

```json
{
  "url": "https://www.youtube.com/watch?v=...",
  "lang": "en",
  "format": "text",
  "timeout_ms": 60000
}
```

`lang` defaults to `en`, and `format` defaults to `text`. Set `format` to `vtt` to return timestamped WebVTT cues, which are useful for prompts such as creating a timestamped table of contents. Manual subtitles are not requested; this uses `--write-auto-subs`.

## Server

- MCP endpoint: `http://localhost:3000/mcp`
- Health check: `GET http://localhost:3000/healthz`
- No authentication; deploy only behind your trusted boundary.

Configuration can be supplied with CLI flags or environment variables:

```sh
yt-dlp-mcp --host 0.0.0.0 --port 3000
YT_DLP_MCP_PORT=3000 yt-dlp-mcp
```

Relevant env vars:

- `YT_DLP_MCP_HOST` default `0.0.0.0`
- `YT_DLP_MCP_PORT` default `3000`
- `YT_DLP_MCP_DEFAULT_TIMEOUT` default `60s`
- `YT_DLP_MCP_MAX_TIMEOUT` default `5m`
- `YT_DLP_MCP_MAX_CONCURRENCY` default `2`

## Docker

```sh
just build-image
# or plain docker build:
docker build -t yt-dlp-mcp .

docker run --rm -p 3000:3000 yt-dlp-mcp
```

The runtime image is based on `python:3.14-slim`, installs `yt-dlp[default]`, and copies `deno` for yt-dlp's JavaScript token handling.

Published images include max-level provenance, an SBOM, and a keyless cosign signature. Nightly release tarballs include `SHA256SUMS` and GitHub artifact attestations.

Verify a published image:

```sh
cosign verify ghcr.io/<owner>/<repo>:latest \
  --certificate-identity-regexp 'https://github.com/<owner>/<repo>/.github/workflows/publish-image.yaml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Verify a downloaded nightly tarball:

```sh
sha256sum -c SHA256SUMS
gh attestation verify yt-dlp-mcp-linux-amd64.tar.gz --repo <owner>/<repo>
```

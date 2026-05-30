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

Download auto-generated subtitles in `json3` format and return normalized plain text.

```json
{
  "url": "https://www.youtube.com/watch?v=...",
  "lang": "en",
  "timeout_ms": 60000
}
```

`lang` defaults to `en`. Manual subtitles are not requested; this uses `--write-auto-subs`.

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

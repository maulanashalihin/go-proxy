# go-proxy

Generic reverse proxy + disk cache for any website. All traffic (`html, css, js, pdf, jpg/png`) goes through this service, then gets cached.

Example: `TARGET_URL=https://dulak.pages.dev` -> open `http://localhost:8080/` and you get the origin content, with absolute URLs rewritten to relative paths so assets keep flowing through the proxy.

## Config via `.env`

| Key | Default | Description |
|---|---|---|
| `TARGET_URL` | required | Target website, e.g. `https://dulak.pages.dev` |
| `PORT` | `8080` | Port this proxy service listens on |
| `CACHE_DIR` | `./cache` | Disk cache directory |
| `CACHE_ENABLED` | `true` | `false` = proxy only, no caching |
| `CACHE_MAX_SIZE_MB` | `1024` | Evict oldest files when over limit |
| `TTL_HTML` | `5m` |  |
| `TTL_CSS` | `1h` |  |
| `TTL_JS` | `1h` |  |
| `TTL_IMAGE` | `24h` | `image/*` |
| `TTL_PDF` | `7d` |  |
| `TTL_DEFAULT` | `10m` | everything else |
| `REWRITE_HTML/CSS/JS` | `true/true/false` | rewrite absolute origin URLs -> `/...` |
| `ORIGIN_TIMEOUT` | `30s` |  |
| `PURGE_TOKEN` | `changeme` | token for `/__purge` endpoints |

Real environment variables always win over `.env` file values.

## Run

```bash
cp .env.example .env
# edit TARGET_URL and PORT
go run .
# open http://localhost:8080/
```

Build:

```bash
go build -o go-proxy .
TARGET_URL=https://dulak.pages.dev PORT=8080 ./go-proxy
```

Docker:

```bash
docker build -t go-proxy .
docker run -p 8080:8080 -e TARGET_URL=https://dulak.pages.dev -e PORT=8080 go-proxy
```

## How it works

- `GET /path?q` -> `TARGET_URL/path?q`. `POST/PUT/DELETE` are proxied without caching.
- `Range` requests (PDF viewers) bypass the cache and stream directly.
- `html/css` (<15MB) are buffered, absolute origin URLs (`https://target/...`, `http://...`, `//host/...`) are rewritten to `/...`, then stored.
- `pdf/jpg/png` etc. are streamed + `tee`'d to the cache file, keeping RAM usage low.
- `Location` redirects are rewritten to proxy paths. `Set-Cookie Domain=target` is stripped so cookies stick to the proxy host.
- `Accept-Encoding: identity` is sent to the origin for consistent rewriting/caching.
- Not cached: non-200 status, `Cache-Control: no-store`, `Authorization` header, HTML with `Set-Cookie` (session).
- Cache layout: `./cache/<sha256>.meta` + `.body`. Expired entries are removed lazily + by a 10-minute janitor. Over capacity removes the oldest files.

Extra headers: `X-Cache: HIT/MISS/BYPASS`, `X-Proxy-Target`.

## Internal endpoints

- `GET /__health` -> `{ok, target, port}`
- `GET /__purge?url=/path&token=...` purge a single URL (accepts path or full URL)
- `GET /__purge-all?token=...` purge everything

## Change target without restart? No.

Edit `TARGET_URL`/`PORT` in `.env` then restart. One process = one target (so cache keys never mix).

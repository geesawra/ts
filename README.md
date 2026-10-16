# ts — torrent search

A small Go web app for searching Jackett and sending results to named Transmission instances. All UI assets, including Bootstrap **6.0.0-alpha.1**, are embedded in one binary. No Node runtime, CDN, or database is needed.

Upstream integrations reuse [`webtor-io/go-jackett`](https://github.com/webtor-io/go-jackett) and [`hekmon/transmissionrpc/v3`](https://github.com/hekmon/transmissionrpc). App adapters preserve reverse-proxy paths, advanced query parameters, result metadata, duplicate reporting, response limits and credential-safe errors.

## Run locally

Requires Go 1.27 or later:

```sh
cp config.example.toml config.toml
# Edit config.toml: Jackett URL/API key, Transmission RPC URLs and credentials.
go build -trimpath -o ts .
./ts -config config.toml
```

Open `http://127.0.0.1:8080`. Configuration is loaded at startup; restart after editing. The TOML file is the configuration interface; there is no browser-based credentials editor. See `config.example.toml` for every setting. Unknown keys and invalid configurations fail startup.

## Docker

```sh
docker build -t ts:local .
# Set [server] listen = "0.0.0.0:8080" in config.toml first.
docker run -d --name ts --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src="$(pwd)/config.toml",dst=/config/config.toml,readonly \
  ts:local
```

The container runs as UID 10001. Its config must be readable by that UID; prefer a dedicated group/read-only mount rather than making credentials world-readable. The image contains CA certificates for HTTPS upstreams. Only the configuration file is mounted; downloads are performed by Transmission, not ts. `download_dir` is a path **on the Transmission host**.

Inside Docker, `localhost` means the ts container, not your host or another container. Use reachable LAN addresses or container DNS names on a shared Docker network. For host services on Linux you can add `--add-host=host.docker.internal:host-gateway` and use that hostname. Jackett download links must use the same configured Jackett origin/base path; configure Jackett's external URL/reverse proxy accordingly.

### Published images and CI

GitHub Actions runs Go race-enabled tests and `go vet`, then builds a Linux amd64 image. Pushes to `main` publish `ghcr.io/geesawra/ts:latest` and a `sha-<commit>` tag. Version tags such as `v1.2.3` also publish `:1.2.3` and `:1.2`. Pull requests build without publishing; the workflow can also be started manually.

The workflow authenticates to GHCR using its repository-scoped `GITHUB_TOKEN` with `packages: write`; no repository secret is required. The repository and newly created package are private by default. To pull the private image locally, log in with a GitHub personal access token that has `read:packages`:

```sh
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u geesawra --password-stdin
docker pull ghcr.io/geesawra/ts:latest
```

Use `ghcr.io/geesawra/ts:latest` instead of `ts:local` in the Docker run command above. After pulling an updated image, recreate the container to deploy it. Keep `config.toml` local; it is ignored by Git and excluded from the Docker build context.


## Search and download

- Discovers configured Jackett indexers, categories/subcategories and Torznab capabilities.
- Choose indexers and categories with aligned checkbox lists; no checked categories means all categories. Normal search needs only a query (leave it empty for recent releases). Search mode, tracker capabilities, ID/season/episode fields and limit/offset controls are grouped inside the collapsed **Advanced search options** section.
- Results show tracker, size, seeders/peers, age, categories and download/upload volume factors. Sort/filter the returned set locally; limit/offset query the next upstream result window. Trackers may ignore unsupported parameters or pagination; these are Torznab windows, not a guaranteed global merged cursor.
- Individual tracker failures are reported alongside successful results.
- Each result has one Transmission destination dropdown and an **Add** button. The selected destination survives sorting and filtering of the current result page. Adds start immediately; the selector is locked while an add is pending. Existing torrents are reported as duplicates, not added twice.
- Magnet results go directly to Transmission. Torrent-only results are fetched by ts from Jackett, then submitted as base64 metainfo; Transmission never needs the Jackett API key.
- Search result download tokens expire after 30 minutes or on restart. Search again if an action reports expiration. Cache capacity is 20,000 results; new searches are rejected temporarily if full.
- Download resolution follows up to five HTTP redirects within the configured Jackett origin/base path and handles `Location: magnet:...` by sending the resolved magnet directly to Transmission. HTTP redirects to another origin or outside the configured base path are rejected. Torrent responses are limited to 16 MiB. The `.torrent` link forwards a resolved magnet redirect to the browser rather than treating it as torrent bytes.

## Security and network access

Default binding is loopback. Set `[server] username` and `password` together to protect the UI/API with HTTP Basic authentication. Use HTTPS through a trusted reverse proxy when accessing over a network; Basic authentication is not encryption. Do not expose an unauthenticated instance to the internet: anyone with access can search private trackers and start downloads.

Credentials remain server-side. The browser receives opaque, expiring result IDs rather than Jackett download URLs/API keys. Magnet links can contain private tracker passkeys; treat copied magnets as secrets. Mutation endpoints require JSON and reject cross-origin browser requests. The app has no CORS allowance. `/healthz` is unauthenticated and returns only `ok`. Reverse-proxy at the origin root (subpath hosting is not supported).

Bootstrap 6 is **alpha software**. The UI follows the OS light/dark preference. Upstream's browser floor is Chrome/Edge 130+, Firefox 132+, Safari 18+; see the [Bootstrap 6 announcement](https://blog.getbootstrap.com/2026/10/08/bootstrap-6-alpha/). Bootstrap's MIT license is included with the vendored CSS.

## Checks

```sh
go test ./...
go vet ./...
```

Real deployment requires reachable Jackett and Transmission endpoints. Tests use isolated HTTP servers and do not contact trackers or download content.

The initial implementation was smoke-checked as a native binary and a non-root Docker container against local Jackett/Torznab and Transmission protocol fixtures. Real Chromium exercised search, both add paths, advanced TV parameters, pagination, URL restoration, filtering, pending downloads during sorting, and light/dark/mobile rendering. These checks do not establish compatibility with your particular live trackers or daemon configuration.

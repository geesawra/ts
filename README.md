# ts — torrent search

Search Jackett and send torrents to named Transmission instances. A single Go binary with a simple, light/dark-aware Bootstrap 6 UI.

- Tracker/category filters, optional advanced search, sorting and pagination. Native result cards on smaller screens; a table on desktop.
- Optional OMDb-backed season search: aggregate tracker pages on the server, inspect parsed release components, choose a pack or episode releases, and add them sequentially.
- Configure everything in TOML; credentials stay server-side.

> **Development disclaimer:** This program was developed with LLMs under explicit direction from a human. Review it before use; no warranty is provided.

## Run

Requires Go 1.27 or later.

```sh
cp config.example.toml config.toml
# Edit config.toml with your Jackett API key and Transmission endpoints.
go build -o ts .
./ts -config config.toml
```

Open **http://127.0.0.1:8080**. Restart after configuration changes. `config.toml` is ignored by Git.

## Optional complete-season search

Add your own OMDb key to `config.toml`, then restart:

```toml
[omdb]
api_key = "your-omdb-key"
# timeout_seconds = 60
```

Without a key, metadata and season-search controls stay hidden; ordinary torrent search works unchanged. This key is separate from Jackett's configuration and stays server-side. Successful metadata is cached for 15 minutes (up to 256 entries).

1. Enter a show title and tick **Show complete seasons** in General search or TV mode.
2. Click **Find series**, then choose the exact title/year/IMDb identity. Optionally enter a season number; blank searches all seasons.
3. Search, expand a season proposal, and inspect the original torrent titles, parsed episodes, release group, quality, tracker, seeds, and size.
4. Choose a season pack or individual episode candidates and a Transmission destination. Multi-episode torrents are added once; per-release outcomes report additions, duplicates, and failures.

Aggregation starts at offset zero with 100 results per request, up to 10 pages per tracker and 50 pages overall, with a two-minute search deadline. Errors, repeated pages, and page limits mark tracker coverage as partial. Cancel search aborts discovery. Download batches already started continue while you search again; keep the page open for pending adds.

**Coverage is not proof of a finished season:** proposals compare downloadable episode markers with OMDb's current inventory, including future/unknown-date episodes. Season-pack contents remain unverified. Missing or invalid metadata leaves completeness unknown. Release matching is conservative (normalized show title, optionally its first year); ambiguous titles remain available in the original result list. Special-season torrents are not mixed into numbered OMDb season proposals. Result-list filters do not alter season proposals or their coverage.


## Docker

Set `server.listen = "0.0.0.0:8080"` in your config, then:

```sh
docker run -d --name ts --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src="$(pwd)/config.toml",dst=/config/config.toml,readonly \
  ghcr.io/geesawra/ts:latest
```

The config must be readable by container UID **10001**. Use reachable hostnames for Jackett/Transmission—`localhost` inside Docker refers to the container. Download directories are paths on the Transmission host.

To build locally: `docker build -t ts:local .`. If GHCR requests authentication, use `docker login ghcr.io` with a token that has `read:packages`.

## CI

GitHub Actions runs Go tests and `go vet`, then publishes Linux amd64 images to **ghcr.io/geesawra/ts**. `main` publishes `latest`; every publish includes a commit tag, and `v1.2.3` publishes `1.2.3` and `1.2`. Pull requests build without publishing.

## Notes

- Keep the app private, or enable the optional web username/password and use HTTPS.
- The page uses `Referrer-Policy: same-origin`: local API POSTs retain their origin, while cross-origin links receive no referrer. Opaque (`Origin: null`) and cross-origin download requests remain blocked.
- Uses [go-jackett](https://github.com/webtor-io/go-jackett) and [transmissionrpc](https://github.com/hekmon/transmissionrpc).
- Bootstrap 6 is currently alpha and requires a modern browser. Assets are bundled; no CDN is needed.

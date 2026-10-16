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
3. Search and expand a season proposal. Choose a bundle by resolution, source, codec, and release group; its episode coverage, gaps, and selected torrent count are shown without listing every episode.
4. Expand **Inspect or override individual torrents**, then an episode, to inspect original titles, parsed components, tracker, seeds, and size; choose an alternative or skip it. Review the selected bundle or season pack, then select a Transmission destination and add it. Multi-episode torrents are added once; per-release outcomes report additions, duplicates, and failures.

Season discovery combines the broad series-title query with targeted queries for each selected season (`S09`) and every episode in its OMDb inventory (`S09E01`, etc.). Broad-query pagination alone is insufficient: some trackers expose only their latest releases, and EZTV strips season-only search markers. Episode-specific queries recover older releases that never appear in those broader result sets. Up to four queries run concurrently; results are deduplicated across queries while alternatives from different trackers remain inspectable. Missing episode metadata leaves episode-specific discovery unavailable and is reported as partial coverage.

Every query fetches all pages exposed by each selected Jackett tracker: it starts at offset zero, requests 100 rows per call, and advances by the raw row count actually returned. It stops at an advertised Torznab total or an empty page—not at a fixed page count, result count, or whole-search deadline. An exhausted query does not prove the tracker exposed its entire catalog. Per-request Jackett timeouts still apply. Errors, repeated pages, and inconsistent pagination are reported as partial coverage. Cancel search aborts discovery. Large season searches require more requests and proportionally more memory; their download tokens expire after 30 minutes. Download batches already started continue while you search again; keep the page open for pending adds.

**Find series** follows OMDb's `totalResults` through all advertised pages. OMDb exposes at most 100 pages (1,000 matches); broader searches require a narrower query. Failed or incomplete multi-page lookups are reported as errors and are not cached as successful results.

Season bundles group downloadable episode releases by resolution, source, codec, and case-insensitive release group. Equivalent spellings such as `WEB.DL`/`WEB-DL` and `H264`/`AVC` are combined; encoder labels such as `x264` remain distinct. Unknown components are labeled explicitly. Each bundle selects higher-seeded alternatives while avoiding overlapping episode coverage where possible. Mirrors remain inspectable alternatives but do not inflate episode coverage. A specific bundle never silently fills gaps with another quality or group: its missing episodes remain visible.

Initial choices favor the recognized release group covering the most distinct downloadable inventory episodes. Group-coverage ties use group-name order. The default starts with that group's best-covered quality bundle, with seed counts breaking bundle-coverage ties; the separate **Recommended selection** fills gaps using other recommended releases. Mixed qualities/groups are listed in the selected composition. Automatic bundles use OMDb's episode inventory when available; episodes outside that inventory remain individually selectable in the inspector. With no inventory, parsed episode numbers are used and completeness remains unknown. Manual overrides/skips, expanded inspectors, and episode choices survive rerenders and pack/episode toggles; explicitly choosing another bundle resets its episode choices. Skipping an episode removes any selected multi-episode torrent containing it. Packs remain unverified and are the default only when no individual releases can be recommended.

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

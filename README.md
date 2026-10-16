# ts — torrent search

Search Jackett and send torrents to named Transmission instances. A single Go binary with a simple, light/dark-aware Bootstrap 6 UI.

- Tracker/category filters, optional advanced search, sorting and pagination.
- Choose a Transmission destination, then click **Add**.
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
- Uses [go-jackett](https://github.com/webtor-io/go-jackett) and [transmissionrpc](https://github.com/hekmon/transmissionrpc).
- Bootstrap 6 is currently alpha and requires a modern browser. Assets are bundled; no CDN is needed.
